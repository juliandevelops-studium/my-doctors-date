package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mfa"
)

// UF-02/UF-03/UF-04: first login yields a setup token, MFA setup unlocks the full scope.
func TestFirstLoginForcesMfaSetup(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(false, "patient")

	// The password step tells the client that there is no MFA yet, so it can skip the code prompt.
	if st, ch := h.do("POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password}); st != 200 || ch["mfa_enabled"] != false {
		t.Fatalf("password step: %d %v", st, ch)
	}

	st, out := h.login(a, "")
	if st != 200 || out["scope"] != "setup" || out["mfa_enabled"] != false {
		t.Fatalf("first login: %d %v", st, out)
	}
	setup := str(out, "access_token")

	// The setup token reaches only the setup routes.
	h.want(200, "GET", "/v1/me", setup, nil)
	h.wantCode(401, "unauthorized", "POST", "/v1/me/mfa/recovery-codes", setup, obj{"code": "123456"})
	h.wantCode(401, "unauthorized", "PATCH", "/v1/me", setup, obj{"first_name": "X"})
	h.wantCode(401, "unauthorized", "POST", "/v1/me/deletion", setup, obj{"password": goodPassword, "code": "123456"})

	start := h.want(200, "POST", "/v1/me/mfa/setup", setup, nil)
	secret, ok := decodeSecret(str(start, "secret"))
	if !ok || !strings.HasPrefix(str(start, "otpauth_uri"), "otpauth://totp/Praxis:") {
		t.Fatalf("setup answer: %v", start)
	}
	h.wantCode(422, "invalid_code", "POST", "/v1/me/mfa/confirm", setup, obj{"code": "000000"})

	code := mfa.Code(secret, mfa.Step(h.now()))
	done := h.want(200, "POST", "/v1/me/mfa/confirm", setup, obj{"code": code})
	if n := len(done["recovery_codes"].([]any)); n != 10 {
		t.Fatalf("recovery codes: %d", n)
	}
	full := done["token"].(obj)
	if full["scope"] != "full" || full["mfa_enabled"] != true {
		t.Fatalf("token after setup: %v", full)
	}
	c := claimsOf(t, h, str(full, "access_token"))
	if c.Scope != praxisauth.ScopeFull || !c.MFA || c.Subject != a.ID.String() || c.Roles[0] != "patient" || c.Expires-c.IssuedAt != 900 {
		t.Fatalf("claims: %+v", c)
	}
	h.want(200, "POST", "/v1/me/mfa/recovery-codes", str(full, "access_token"), obj{"code": h.nextCode(secret)})

	// A second setup attempt is refused (decision 22), and so is replaying the code used for confirmation.
	h.wantCode(409, "mfa_active", "POST", "/v1/me/mfa/setup", str(full, "access_token"), nil)

	// Next login: the TOTP code is mandatory and single use.
	a.Secret = secret
	h.advance(60 * time.Second)
	_, ch, _ := h.req("POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login/mfa", "", obj{"mfa_challenge": ch["mfa_challenge"]})
}

func (h *harness) nextCode(secret []byte) string {
	h.advance(mfa.Period * time.Second)
	return mfa.Code(secret, mfa.Step(h.now()))
}

func decodeSecret(b32 string) ([]byte, bool) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var bits, n uint
	var out []byte
	for _, r := range b32 {
		i := strings.IndexRune(alphabet, r)
		if i < 0 {
			return nil, false
		}
		bits = bits<<5 | uint(i)
		n += 5
		if n >= 8 {
			out = append(out, byte(bits>>(n-8)))
			n -= 8
		}
	}
	return out, len(out) == 20
}

func TestLoginWithMfa(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "doctor")

	st, out := h.login(a, h.code(a))
	if st != 200 || out["scope"] != "full" || out["mfa_enabled"] != true {
		t.Fatalf("login: %d %v", st, out)
	}
	token := str(out, "access_token")
	me := h.want(200, "GET", "/v1/me", token, nil)
	if me["email"] != a.Email || me["id"] != a.ID.String() {
		t.Fatalf("me: %v", me)
	}

	// The same code cannot be used twice (RFC 6238 replay protection).
	h.advance(-30 * time.Second)
	code := mfa.Code(a.Secret, mfa.Step(h.now()))
	if st, _ := h.login(a, code); st != 401 {
		t.Fatalf("replayed code accepted: %d", st)
	}
	// Missing and wrong codes are refused.
	if st, _ := h.login(a, ""); st != 401 {
		t.Fatalf("login without code: %d", st)
	}
	if st, _ := h.login(a, "000000"); st != 401 {
		t.Fatalf("wrong code: %d", st)
	}

	// The challenge is single use and only valid for the MFA step.
	h.advance(90 * time.Second)
	_, ch, _ := h.req("POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
	good := obj{"mfa_challenge": ch["mfa_challenge"], "code": mfa.Code(a.Secret, mfa.Step(h.now()))}
	h.want(200, "POST", "/v1/auth/login/mfa", "", good)
	good["code"] = mfa.Code(a.Secret, mfa.Step(h.now())+1)
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login/mfa", "", good)
	h.wantCode(401, "unauthorized", "GET", "/v1/me", str(ch, "mfa_challenge"), nil)

	// Logout revokes the token.
	h.want(204, "POST", "/v1/auth/logout", token, nil)
	h.wantCode(401, "unauthorized", "GET", "/v1/me", token, nil)
}

func TestRecoveryCodeLogin(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)
	codes := h.want(200, "POST", "/v1/me/mfa/recovery-codes", token, obj{"code": h.code(a)})["recovery_codes"].([]any)

	login := func(code string) int {
		_, ch, _ := h.req("POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
		st, _ := h.do("POST", "/v1/auth/login/mfa", "", obj{"mfa_challenge": ch["mfa_challenge"], "recovery_code": code})
		return st
	}
	if st := login(codes[0].(string)); st != 200 {
		t.Fatalf("recovery code: %d", st)
	}
	if st := login(codes[0].(string)); st != 401 {
		t.Fatalf("a recovery code must work once: %d", st)
	}
	if st := login(strings.ToUpper(codes[1].(string))); st != 200 {
		t.Fatalf("recovery codes ignore the case: %d", st)
	}
	// Regenerating invalidates the old codes.
	h.want(200, "POST", "/v1/me/mfa/recovery-codes", token, obj{"code": h.code(a)})
	if st := login(codes[2].(string)); st != 401 {
		t.Fatalf("old recovery code after regeneration: %d", st)
	}
}

// SR.05: 3rd failure -> 1 minute, each further failure -> 10 minutes, 10th -> permanent, only the admin unlocks.
func TestLoginLockoutSR05(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	wrong := obj{"email": a.Email, "password": "Falsches-Passwort-123456"}
	right := obj{"email": a.Email, "password": a.Password}
	attempt := func(b obj) int { st, _ := h.do("POST", "/v1/auth/login", "", b); return st }

	for i := 1; i <= 2; i++ {
		if attempt(wrong) != 401 {
			t.Fatal("wrong password accepted")
		}
	}
	if attempt(right) != 200 {
		t.Fatal("two failures must not lock")
	}
	h.token(a) // a complete login resets the counter

	for i := 1; i <= 3; i++ {
		attempt(wrong)
	}
	until := h.lockedUntil(a)
	if d := time.Until(until); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("3rd failure locks for %v, want about one minute", d)
	}
	if attempt(right) != 401 {
		t.Fatal("after the 3rd failure even the right password must be refused")
	}
	h.jumpTo(until.Add(-20 * time.Second))
	if attempt(right) != 401 {
		t.Fatal("still locked 20 s before the end")
	}
	h.jumpTo(until.Add(2 * time.Second))
	if attempt(right) != 200 {
		t.Fatal("the one-minute lock must have ended")
	}

	// 4th failure: ten minutes.
	attempt(wrong)
	until = h.lockedUntil(a)
	if d := time.Until(until); d < 9*time.Minute || d > 11*time.Minute {
		t.Fatalf("4th failure locks for %v, want about ten minutes", d)
	}
	h.jumpTo(until.Add(-5 * time.Minute))
	if attempt(right) != 401 {
		t.Fatal("ten-minute lock must still hold after 5 minutes")
	}
	h.jumpTo(until.Add(2 * time.Second))
	if attempt(right) != 200 {
		t.Fatal("ten-minute lock must have ended")
	}

	// Failures 5 to 10; each lock is waited out before the next attempt.
	for i := 5; i <= 10; i++ {
		attempt(wrong)
		if i < 10 {
			h.jumpTo(h.lockedUntil(a).Add(2 * time.Second))
		}
	}
	h.advance(24 * time.Hour)
	if st, out := h.do("POST", "/v1/auth/login", "", right); st != 401 || out["code"] != "invalid_credentials" {
		t.Fatalf("after the 10th failure the account must stay locked, with a neutral answer: %d %v", st, out)
	}

	// Only a system administrator unlocks.
	h.jumpTo(time.Now())
	patient := h.makeUser(true, "patient")
	h.wantCode(403, "forbidden", "POST", "/v1/admin/users/"+a.ID.String()+"/unlock", h.token(patient), nil)
	admin := h.makeUser(true, "systemadmin")
	h.want(204, "POST", "/v1/admin/users/"+a.ID.String()+"/unlock", h.token(admin), nil)
	if attempt(right) != 200 {
		t.Fatal("unlocked account must log in")
	}
}

func TestWrongTotpCountsAsFailure(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	for i := 0; i < 3; i++ {
		if st, _ := h.login(a, "000000"); st != 401 {
			t.Fatal("wrong code accepted")
		}
	}
	// Now locked: even the right password and code are refused.
	if st, _ := h.login(a, h.code(a)); st != 401 {
		t.Fatalf("login during the lock: %d", st)
	}
	h.jumpTo(h.lockedUntil(a).Add(2 * time.Second))
	if st, _ := h.login(a, h.code(a)); st != 200 {
		t.Fatalf("login after the lock: %d", st)
	}
}

// Decision 22: MFA can only be replaced by removing it first.
func TestMfaDisableAndReplace(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)

	h.wantCode(409, "mfa_active", "POST", "/v1/me/mfa/setup", token, nil)
	h.wantCode(422, "invalid_credentials", "POST", "/v1/me/mfa/disable", token, obj{"password": a.Password, "code": "000000"})
	h.wantCode(422, "invalid_credentials", "POST", "/v1/me/mfa/disable", token, obj{"password": "Falsches-Passwort-123456", "code": h.code(a)})
	h.wantCode(422, "invalid_credentials", "POST", "/v1/me/mfa/disable", token, obj{"password": a.Password}) // no code
	// Three failures lock the account (SR.05): even a correct attempt is refused until the lock has ended.
	h.wantCode(429, "temporarily_locked", "POST", "/v1/me/mfa/disable", token, obj{"password": a.Password, "code": h.code(a)})
	h.jumpTo(h.lockedUntil(a).Add(2 * time.Second))

	h.want(204, "POST", "/v1/me/mfa/disable", token, obj{"password": a.Password, "code": h.code(a)})
	h.wantCode(401, "unauthorized", "GET", "/v1/me", token, nil) // all sessions ended

	// The next login only gives a setup token; MFA can be set up again with a new secret.
	st, out := h.login(a, "")
	if st != 200 || out["scope"] != "setup" || out["mfa_enabled"] != false {
		t.Fatalf("login after removal: %d %v", st, out)
	}
	setup := str(out, "access_token")
	start := h.want(200, "POST", "/v1/me/mfa/setup", setup, nil)
	secret, _ := decodeSecret(str(start, "secret"))
	if string(secret) == string(a.Secret) {
		t.Fatal("a new setup must create a new secret")
	}
	done := h.want(200, "POST", "/v1/me/mfa/confirm", setup, obj{"code": mfa.Code(secret, mfa.Step(h.now()))})
	h.want(200, "GET", "/v1/me", str(done["token"].(obj), "access_token"), nil)
	// A user without MFA never holds a full token, so the disable route is out of reach for a setup token.
	h.wantCode(401, "unauthorized", "POST", "/v1/me/mfa/disable", setup, obj{"password": a.Password, "code": "123456"})
}

func TestMfaDisableWithRecoveryCode(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)
	codes := h.want(200, "POST", "/v1/me/mfa/recovery-codes", token, obj{"code": h.code(a)})["recovery_codes"].([]any)
	h.want(204, "POST", "/v1/me/mfa/disable", token, obj{"password": a.Password, "recovery_code": codes[0]})
}
