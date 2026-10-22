// Package praxisauth holds the token code shared by all backend services: service tokens (api/internal.md section 3),
// user JWTs (section 1) and JWKS (section 2). Only dependency besides the standard library: google/uuid.
package praxisauth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Definition of token lifetime, clock drift (tolerance)
const (
	MaxServiceTokenLifetime = 60 * time.Second
	clockSkew               = 5 * time.Second
)

// ErrInvalid is used for logging error messages
var ErrInvalid = errors.New("invalid token")

// ErrReplay is returned when a token with an already seen jti is presented again. It wraps ErrInvalid.
var ErrReplay = fmt.Errorf("%w: replayed", ErrInvalid)

// ServiceClaims holds JWT claims with issuer, audience, expiry information, unique token id (JIT), and
type ServiceClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
	JTI      string `json:"jti"`
	Act      string `json:"act,omitempty"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ,omitempty"`
	Kid string `json:"kid,omitempty"`
}

// ServiceVerifier checks service tokens against the public keys of the known callers.
type ServiceVerifier struct {
	audience string
	keys     map[string]ed25519.PublicKey
	now      func() time.Time
	replay   *replayCache
}

// NewServiceVerifier creates a verifier. keys maps the issuer (caller name) to its public key.
// Every token is accepted only once (see ErrReplay).
// It returns an error if audience is empty.
func NewServiceVerifier(audience string, keys map[string]ed25519.PublicKey) (*ServiceVerifier, error) {
	if len(audience) == 0 {
		return nil, errors.New("praxisauth: audience must not be empty")
	}
	return &ServiceVerifier{audience: audience, keys: keys, now: time.Now, replay: newReplayCache()}, nil
}

// SetClock replaces the clock (tests).
func (v *ServiceVerifier) SetClock(now func() time.Time) { v.now = now }

// Verify checks a compact JWT and returns its claims.
// Header must contain EdDSA as digital signature algorithm.
// Issuer must be known -> must be in ServiceVerifier keys map.
// Signature must be correct.
// Audience must be correct.
// Token must not be expired or invalid in any other way.
func (v *ServiceVerifier) Verify(token string) (*ServiceClaims, error) {
	parts, h, err := split(token)
	if err != nil {
		return nil, err
	}
	if h.Alg != "EdDSA" {
		return nil, fmt.Errorf("%w: alg", ErrInvalid)
	}
	var c ServiceClaims
	if err := decodeJSON(parts[1], &c); err != nil {
		return nil, fmt.Errorf("%w: claims", ErrInvalid)
	}
	key, ok := v.keys[c.Issuer]
	if !ok {
		return nil, fmt.Errorf("%w: unknown issuer", ErrInvalid)
	}
	if !verifySig(key, parts) {
		return nil, fmt.Errorf("%w: signature", ErrInvalid)
	}
	if c.Audience != v.audience {
		return nil, fmt.Errorf("%w: audience", ErrInvalid)
	}
	now := v.now()
	iat, exp := time.Unix(c.IssuedAt, 0), time.Unix(c.Expires, 0)
	switch {
	case c.IssuedAt == 0 || c.Expires == 0:
		return nil, fmt.Errorf("%w: iat/exp missing", ErrInvalid)
	case exp.Sub(iat) > MaxServiceTokenLifetime || !exp.After(iat):
		return nil, fmt.Errorf("%w: lifetime", ErrInvalid)
	case iat.After(now.Add(clockSkew)):
		return nil, fmt.Errorf("%w: issued in the future", ErrInvalid)
	case !exp.After(now.Add(-clockSkew)):
		return nil, fmt.Errorf("%w: expired", ErrInvalid)
	case c.JTI == "":
		return nil, fmt.Errorf("%w: jti missing", ErrInvalid)
	}
	if !v.replay.firstUse(c.Issuer, c.JTI, exp.Add(clockSkew), now) {
		return nil, ErrReplay
	}
	return &c, nil
}

// MintServiceToken creates a service token signed with priv.
// Used by a service to authenticate itself to another service.
func MintServiceToken(priv ed25519.PrivateKey, issuer, audience, act string, now time.Time, jti string) string {
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT"})
	c, _ := json.Marshal(ServiceClaims{Issuer: issuer, Audience: audience, IssuedAt: now.Unix(), Expires: now.Add(MaxServiceTokenLifetime).Unix(), JTI: jti, Act: act})
	return sign(priv, h, c)
}

// ServiceSigner mints service tokens for one calling service.
type ServiceSigner struct {
	issuer string
	priv   ed25519.PrivateKey
}

// NewServiceSigner creates a signer for the calling service issuer.
func NewServiceSigner(issuer string, priv ed25519.PrivateKey) *ServiceSigner {
	return &ServiceSigner{issuer: issuer, priv: priv}
}

// Token returns a fresh token for the callee audience. act is the end user on whose behalf the call is made (audit only).
func (s *ServiceSigner) Token(audience, act string) string {
	return MintServiceToken(s.priv, s.issuer, audience, act, time.Now(), uuid.NewString())
}

// LoadPublicKeys reads <name>.pub (PEM, PKIX Ed25519) files from dir. The file name without .pub is the key owner.
func LoadPublicKeys(dir string) (map[string]ed25519.PublicKey, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.pub"))
	if err != nil {
		return nil, err
	}
	keys := map[string]ed25519.PublicKey{}
	for _, f := range files {
		der, err := readPEM(f)
		if err != nil {
			return nil, err
		}
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		ed, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%s: not an Ed25519 key", f)
		}
		keys[strings.TrimSuffix(filepath.Base(f), ".pub")] = ed
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no *.pub files in %s", dir)
	}
	return keys, nil
}

// LoadPrivateKey reads an Ed25519 private key (PEM, PKCS#8).
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	der, err := readPEM(path)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return ed, nil
}

// readPEM reads the file at path and returns the DER bytes of its first PEM block.
func readPEM(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-controlled key directory / secret mount
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	return block.Bytes, nil
}

func split(token string) ([]string, header, error) {
	var h header
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, h, fmt.Errorf("%w: format", ErrInvalid)
	}
	if err := decodeJSON(parts[0], &h); err != nil {
		return nil, h, fmt.Errorf("%w: header", ErrInvalid)
	}
	return parts, h, nil
}

func verifySig(key ed25519.PublicKey, parts []string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig)
}

func sign(priv ed25519.PrivateKey, headerJSON, claimsJSON []byte) string {
	signing := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(signing)))
}

func decodeJSON(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
