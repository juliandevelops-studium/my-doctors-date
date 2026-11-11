// Package mfa implements TOTP (RFC 6238), the encryption of the stored secret and recovery codes.
package mfa

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default HMAC for authenticator apps
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Period is the TOTP time step in seconds.
	Period = 30
	// Digits is the length of a code.
	Digits = 6
	// RecoveryCodeCount is the number of recovery codes issued at once.
	RecoveryCodeCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret.
func NewSecret() []byte {
	s := make([]byte, 20)
	if _, err := rand.Read(s); err != nil {
		panic(err)
	}
	return s
}

// EncodeSecret returns the Base32 form shown to the user.
func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// URI returns the otpauth URI for authenticator apps.
func URI(issuer, account string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(Period))
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + q.Encode()
}

// Code computes the code of one time step.
func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // step is positive
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, v%1_000_000)
}

// Step returns the time step of t.
func Step(t time.Time) int64 { return t.Unix() / Period }

// Verify checks a code against the steps -1, 0 and +1 around now (clock drift) and returns the matching step.
// The caller must still make the step single-use (mfaUseStep).
func Verify(secret []byte, code string, now time.Time) (int64, bool) {
	if len(code) != Digits {
		return 0, false
	}
	cur := Step(now)
	var found int64
	for d := int64(-1); d <= 1; d++ {
		if subtle.ConstantTimeCompare([]byte(Code(secret, cur+d)), []byte(code)) == 1 {
			found = cur + d
		}
	}
	return found, found != 0
}

// Encrypt seals the secret with AES-256-GCM. The user id is bound as additional data so that a ciphertext cannot be
// moved to another account. The nonce is prepended.
func Encrypt(key []byte, userID string, plain []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte(userID)), nil
}

// Decrypt opens a value made by Encrypt.
func Decrypt(key []byte, userID string, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(userID))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alike characters

// NewRecoveryCodes returns RecoveryCodeCount random codes of the form xxxx-xxxx-xxxx (about 59 bits each).
func NewRecoveryCodes() []string {
	out := make([]string, RecoveryCodeCount)
	for i := range out {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		var sb strings.Builder
		for j, x := range b {
			if j > 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(x)%len(recoveryAlphabet)])
		}
		out[i] = sb.String()
	}
	return out
}

// HashRecoveryCode returns the keyed hash that is stored. Input is normalised (case, dashes, spaces).
func HashRecoveryCode(key []byte, code string) string {
	norm := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(code))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("recovery:" + norm))
	return hex.EncodeToString(mac.Sum(nil))
}
