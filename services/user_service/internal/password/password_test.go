package password

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// varied returns n characters without a repeating block.
func varied(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!$%&"
	var b strings.Builder
	x := uint32(12345)
	for b.Len() < n {
		x = x*1664525 + 1013904223
		b.WriteByte(alphabet[(x>>16)%uint32(len(alphabet))])
	}
	return b.String()
}

func codes(v []Violation) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = x.Code
	}
	return out
}

// SR.04: 14 characters rejected, 15 accepted; 512 accepted, 513 rejected.
func TestLengthBoundaries(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		want string // "" = accepted
	}{
		{"14 chars", "kT7#qLm2vXz9wP", "password_too_short"},
		{"15 chars", "kT7#qLm2vXz9wPa", ""},
		{"512 chars", varied(512), ""},
		{"513 chars", varied(513), "password_too_long"},
		{"15 multi-byte characters", "äöüßäöüßäöüßäöü", "password_blocklisted"}, // length counts characters, not bytes; repeats
		{"empty", "", "password_too_short"},
	}
	for _, c := range cases {
		got := codes(Validate(c.pw, ""))
		if c.want == "" && len(got) != 0 || c.want != "" && (len(got) == 0 || got[0] != c.want) {
			t.Errorf("%s: got %v, want %q", c.name, got, c.want)
		}
	}
}

func TestMultiByteLengthIsCountedInCharacters(t *testing.T) {
	pw := "äöüßÄÖÜ-xqzpwkmj" // 16 characters, 23 bytes
	if v := Validate(pw, ""); len(v) != 0 {
		t.Errorf("16 characters must be accepted: %v", codes(v))
	}
	if v := Validate("äöüßÄÖÜ-xqzpw", ""); len(v) == 0 || v[0].Code != "password_too_short" {
		t.Errorf("13 characters (but 19 bytes) must be rejected: %v", codes(v))
	}
}

func TestBlocklistIsLoaded(t *testing.T) {
	if BlocklistSize() < 5000 {
		t.Fatalf("blocklist too small: %d", BlocklistSize())
	}
}

func TestBlocklistedAndWeakPatterns(t *testing.T) {
	var sample string
	for w := range blocklist {
		sample = w
		break
	}
	for name, pw := range map[string]string{
		"list entry":            sample,
		"list entry upper case": strings.ToUpper(sample),
		"padded with spaces":    " " + sample + " ",
		"one character":         strings.Repeat("a", 20),
		"repeated block":        strings.Repeat("hunter2x", 3),
		"repeated word":         "passwordpasswordpassword",
	} {
		v := codes(Validate(pw, ""))
		if len(v) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	if v := codes(Validate("max.mustermann-Orange-Tisch-42", "max.mustermann@example.org")); len(v) != 1 || v[0] != "password_contains_account_name" {
		t.Errorf("password with account name: %v", v)
	}
	if v := Validate("Orange-Tisch-Lampe-Regen-42", "max.mustermann@example.org"); len(v) != 0 {
		t.Errorf("a good passphrase must pass: %v", codes(v))
	}
}

// SR.01: Argon2id with parameters and salt; same password, different hash; verify works.
func TestHashFormatAndSalt(t *testing.T) {
	a, err := Hash("kT7#qLm2vXz9wPa")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Hash("kT7#qLm2vXz9wPa")
	if !strings.HasPrefix(a, "$argon2id$v=19$m=65536,t=3,p=2$") || len(strings.Split(a, "$")) != 6 {
		t.Fatalf("format: %s", a)
	}
	if a == b {
		t.Fatal("two hashes of one password must differ (random salt)")
	}
	if ok, rehash := Verify("kT7#qLm2vXz9wPa", a); !ok || rehash {
		t.Fatalf("verify own hash: %v %v", ok, rehash)
	}
	if ok, _ := Verify("kT7#qLm2vXz9wPb", a); ok {
		t.Fatal("wrong password accepted")
	}
	if ok, _ := Verify(strings.Repeat("a", 513), a); ok {
		t.Fatal("over-long password must not verify")
	}
	if _, err := Hash(strings.Repeat("a", 513)); err == nil {
		t.Fatal("over-long password must not be hashed")
	}
}

func TestVerifyRejectsMalformedAndHostileHashes(t *testing.T) {
	for name, h := range map[string]string{
		"empty":         "",
		"bcrypt":        "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012",
		"plaintext":     "password",
		"huge memory":   "$argon2id$v=19$m=4294967295,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"zero time":     "$argon2id$v=19$m=65536,t=0,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"bad version":   "$argon2id$v=16$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"missing parts": "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0",
		"short salt":    "$argon2id$v=19$m=65536,t=3,p=2$YQ$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if ok, _ := Verify("anything", h); ok {
			t.Errorf("%s verified", name)
		}
	}
}

func TestWeakerParametersAskForRehash(t *testing.T) {
	pw := "kT7#qLm2vXz9wPa"
	salt := []byte("0123456789abcdef")
	weak := fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(argon2.IDKey([]byte(pw), salt, 1, 65536, 2, 32)))
	if ok, rehash := Verify(pw, weak); !ok || !rehash {
		t.Fatalf("a hash with weaker parameters must verify and ask for a rehash: %v %v", ok, rehash)
	}
	h, _ := Hash(pw)
	if ok, rehash := Verify(pw, h); !ok || rehash {
		t.Fatal("current parameters must not ask for a rehash")
	}
}
