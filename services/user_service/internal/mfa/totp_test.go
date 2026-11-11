package mfa

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA-1, 8 digits there; we compare the last 6).
func TestRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := Code(secret, ts/Period); got != want {
			t.Errorf("t=%d: got %s, want %s", ts, got, want)
		}
	}
}

func TestVerifyWindow(t *testing.T) {
	secret := NewSecret()
	now := time.Unix(1_900_000_000, 0)
	cur := Step(now)
	for _, d := range []int64{-1, 0, 1} {
		if step, ok := Verify(secret, Code(secret, cur+d), now); !ok || step != cur+d {
			t.Errorf("offset %d: ok=%v step=%d", d, ok, step)
		}
	}
	for _, d := range []int64{-2, 2, 10} {
		if _, ok := Verify(secret, Code(secret, cur+d), now); ok {
			t.Errorf("offset %d accepted", d)
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000 "} {
		if _, ok := Verify(secret, bad, now); ok && bad != Code(secret, cur) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEncryptBindsUserAndDetectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	secret := NewSecret()
	sealed, err := Encrypt(key, "user-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decrypt(key, "user-1", sealed); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := Decrypt(key, "user-2", sealed); err == nil {
		t.Error("a ciphertext must not open for another user")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := Decrypt(key, "user-1", sealed); err == nil {
		t.Error("tampered ciphertext accepted")
	}
	if _, err := Decrypt(key, "user-1", []byte("short")); err == nil {
		t.Error("short ciphertext accepted")
	}
	if _, err := Encrypt([]byte("short key"), "u", secret); err == nil {
		t.Error("short key accepted")
	}
	a, _ := Encrypt(key, "user-1", secret)
	b, _ := Encrypt(key, "user-1", secret)
	if bytes.Equal(a, b) {
		t.Error("nonce reused")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes()
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 14 || strings.Count(c, "-") != 2 || seen[c] {
			t.Errorf("bad or duplicate code %q", c)
		}
		seen[c] = true
	}
	key := bytes.Repeat([]byte{1}, 32)
	h := HashRecoveryCode(key, codes[0])
	if len(h) != 64 || h != HashRecoveryCode(key, strings.ToUpper(strings.ReplaceAll(codes[0], "-", " "))) {
		t.Error("hash must ignore case, dashes and spaces")
	}
	if h == HashRecoveryCode(bytes.Repeat([]byte{2}, 32), codes[0]) {
		t.Error("hash must depend on the key")
	}
}

func TestURI(t *testing.T) {
	u := URI("Praxis", "max@example.org", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/Praxis:max@example.org?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "issuer=Praxis", "digits=6", "period=30"} {
		if !strings.Contains(u, want) {
			t.Errorf("%q missing in %s", want, u)
		}
	}
}
