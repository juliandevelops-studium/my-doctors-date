package praxisauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// JWK is one Ed25519 public key in a JWKS (RFC 8037).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// NewJWKS builds the key set for the given public keys (key id = KeyID).
func NewJWKS(pubs ...ed25519.PublicKey) JWKS {
	set := JWKS{Keys: []JWK{}}
	for _, p := range pubs {
		set.Keys = append(set.Keys, JWK{Kty: "OKP", Crv: "Ed25519", Kid: KeyID(p), X: base64.RawURLEncoding.EncodeToString(p), Use: "sig", Alg: "EdDSA"})
	}
	return set
}

// ParseJWKS reads a JWKS document into a KeySet. Keys of other types are rejected.
func ParseJWKS(b []byte) (StaticKeySet, error) {
	var set JWKS
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, err
	}
	out := StaticKeySet{}
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			return nil, fmt.Errorf("unsupported key %q", k.Kid)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid key %q", k.Kid)
		}
		out[k.Kid] = ed25519.PublicKey(x)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty key set")
	}
	return out, nil
}
