package praxisauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Audiences of the tokens issued by user_service.
const (
	AudienceAPI = "praxis-api"
	AudienceMFA = "user_service:mfa"
	// UserIssuer is the only valid issuer of user tokens.
	UserIssuer = "user_service"
	// ScopeFull and ScopeSetup are the values of the scope claim (decision 11).
	ScopeFull  = "full"
	ScopeSetup = "setup"
	// AccessTokenLifetime is exp - iat of an access token (decision 12).
	AccessTokenLifetime = 15 * time.Minute
	// MFAChallengeLifetime is exp - iat of the challenge between password and TOTP step.
	MFAChallengeLifetime = 5 * time.Minute
	// userClockSkew is the tolerance for the time claims of user tokens.
	userClockSkew = 30 * time.Second
)

// UserClaims are the claims of an access token or an MFA challenge.
type UserClaims struct {
	Issuer   string   `json:"iss"`
	Audience string   `json:"aud"`
	Subject  string   `json:"sub"`
	Roles    []string `json:"roles,omitempty"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	JTI      string   `json:"jti"`
	MFA      bool     `json:"mfa,omitempty"`
	Scope    string   `json:"scope,omitempty"`
}

// KeySet resolves a key id to a public key.
type KeySet interface {
	Key(kid string) (ed25519.PublicKey, bool)
}

// StaticKeySet is a KeySet backed by a map.
type StaticKeySet map[string]ed25519.PublicKey

// Key implements KeySet.
func (s StaticKeySet) Key(kid string) (ed25519.PublicKey, bool) { k, ok := s[kid]; return k, ok }

// KeyID derives the key id of a public key: the first 8 bytes of its SHA-256, hex encoded.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// NewJTI returns a fresh token id.
func NewJTI() string { return uuid.NewString() }

// SignUser signs a user token. kid must identify the public key of priv in the JWKS.
func SignUser(priv ed25519.PrivateKey, kid string, c UserClaims) string {
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT", Kid: kid})
	cj, _ := json.Marshal(c)
	return sign(priv, h, cj)
}

// VerifyUser checks signature, algorithm, issuer, audience and time claims. The caller must still check scope, mfa
// and the revocation state (tokenCheck).
func VerifyUser(token string, keys KeySet, audience string, now time.Time) (*UserClaims, error) {
	parts, h, err := split(token)
	if err != nil {
		return nil, err
	}
	if h.Alg != "EdDSA" || h.Kid == "" {
		return nil, fmt.Errorf("%w: alg/kid", ErrInvalid)
	}
	key, ok := keys.Key(h.Kid)
	if !ok || !verifySig(key, parts) {
		return nil, fmt.Errorf("%w: key/signature", ErrInvalid)
	}
	var c UserClaims
	if err := decodeJSON(parts[1], &c); err != nil {
		return nil, fmt.Errorf("%w: claims", ErrInvalid)
	}
	iat, exp := time.Unix(c.IssuedAt, 0), time.Unix(c.Expires, 0)
	switch {
	case c.Issuer != UserIssuer:
		return nil, fmt.Errorf("%w: issuer", ErrInvalid)
	case c.Audience != audience:
		return nil, fmt.Errorf("%w: audience", ErrInvalid)
	case c.Subject == "" || c.JTI == "":
		return nil, fmt.Errorf("%w: sub/jti", ErrInvalid)
	case c.IssuedAt == 0 || c.Expires == 0 || !exp.After(iat) || exp.Sub(iat) > AccessTokenLifetime:
		return nil, fmt.Errorf("%w: lifetime", ErrInvalid)
	case iat.After(now.Add(userClockSkew)):
		return nil, fmt.Errorf("%w: issued in the future", ErrInvalid)
	case !exp.After(now.Add(-userClockSkew)):
		return nil, fmt.Errorf("%w: expired", ErrInvalid)
	}
	return &c, nil
}
