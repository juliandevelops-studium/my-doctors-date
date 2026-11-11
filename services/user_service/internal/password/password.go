// Package password implements SR.01 (Argon2id) and SR.04 (password policy).
package password

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Policy limits (SR.04). Length is counted in characters (runes).
const (
	MinLength = 15
	MaxLength = 512
)

// Argon2id parameters, documented in docs/password-blocklist.md.
const (
	memoryKiB   = 64 * 1024
	iterations  = 3
	parallelism = 2
	saltLength  = 16
	keyLength   = 32
	// maxConcurrent bounds the memory used by parallel hash operations (4 x 64 MiB).
	maxConcurrent = 4
)

//go:embed blocklist.txt
var blocklistRaw string

var (
	blocklist = loadBlocklist()
	sem       = make(chan struct{}, maxConcurrent)
)

func loadBlocklist() map[string]struct{} {
	m := map[string]struct{}{}
	for _, l := range strings.Split(blocklistRaw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = struct{}{}
		}
	}
	return m
}

// BlocklistSize returns the number of blocked passwords (tests).
func BlocklistSize() int { return len(blocklist) }

// Violation is one broken rule. Code is stable and goes into the API error.
type Violation struct{ Code string }

// Validate checks the password policy.
func Validate(pw, userEmail string) []Violation {
	var v []Violation
	n := utf8.RuneCountInString(pw)
	switch {
	case n < MinLength:
		return append(v, Violation{"password_too_short"})
	case n > MaxLength:
		return append(v, Violation{"password_too_long"})
	}
	norm := strings.ToLower(strings.TrimSpace(pw))
	if _, bad := blocklist[norm]; bad || repeats(norm) {
		v = append(v, Violation{"password_blocklisted"})
	}
	if local, _, ok := strings.Cut(strings.ToLower(userEmail), "@"); ok && utf8.RuneCountInString(local) >= 6 && strings.Contains(norm, local) {
		v = append(v, Violation{"password_contains_account_name"})
	}
	return v
}

// repeats reports a password made of one repeated character or one repeated short block.
func repeats(s string) bool {
	r := []rune(s)
	for size := 1; size <= len(r)/3 && size <= 8; size++ {
		ok := true
		for i := size; i < len(r); i++ {
			if r[i] != r[i%size] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// Hash returns an Argon2id PHC string with a random salt.
func Hash(pw string) (string, error) {
	if utf8.RuneCountInString(pw) > MaxLength {
		return "", errors.New("password too long")
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, iterations, memoryKiB, parallelism, keyLength)
	<-sem
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks a password against a stored PHC string in constant time. needsRehash is true if the stored
// parameters are weaker than the current ones. A malformed hash never verifies.
func Verify(pw, encoded string) (ok, needsRehash bool) {
	if utf8.RuneCountInString(pw) > MaxLength {
		return false, false
	}
	p, salt, want, err := parse(encoded)
	if err != nil {
		return false, false
	}
	sem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, p.t, p.m, p.p, uint32(len(want))) //nolint:gosec // length of our own hash
	<-sem
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	return true, p.m < memoryKiB || p.t < iterations || p.p < parallelism
}

// DummyVerify burns the time of one verification. Used for unknown accounts so that the response time does not
// reveal whether an e-mail address is registered.
func DummyVerify(pw string) {
	Verify(pw, dummyHash)
}

type params struct {
	m, t uint32
	p    uint8
}

func parse(encoded string) (params, []byte, []byte, error) {
	var p params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errors.New("bad version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.m, &p.t, &p.p); err != nil {
		return p, nil, nil, errors.New("bad parameters")
	}
	// Bounds protect against a manipulated hash that would exhaust memory.
	if p.m < 8 || p.m > 1<<20 || p.t < 1 || p.t > 10 || p.p < 1 || p.p > 16 {
		return p, nil, nil, errors.New("parameters out of range")
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 {
		return p, nil, nil, errors.New("bad salt or key")
	}
	return p, salt, key, nil
}

var dummyHash = func() string {
	h, err := Hash("dummy-password-for-timing-only")
	if err != nil {
		panic(err)
	}
	return h
}()
