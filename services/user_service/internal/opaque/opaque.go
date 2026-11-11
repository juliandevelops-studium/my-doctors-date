// Package opaque creates the one-time tokens of e-mail links. Only the SHA-256 hash is stored.
package opaque

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// New returns a random token (256 bit, 43 characters) and the hash that is stored in the database.
func New() (raw, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, Hash(raw)
}

// Hash returns the stored form of a token.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
