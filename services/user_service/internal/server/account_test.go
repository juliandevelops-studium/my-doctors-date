package server_test

import (
	"strings"
	"testing"
	"time"
)

// UF-12: reset link, neutral answers, sessions end, MFA stays.
func TestPasswordReset(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	old := h.token(a)

	// Unknown and known addresses get the same answer; only the known one gets a mail.
	before := h.mail.count()
	h.want(202, "POST", "/v1/auth/password/forgot", "", obj{"email": uniqueEmail()})
	if h.mail.count() != before {
		t.Fatal("no mail for an unknown address")
	}
	h.want(202, "POST", "/v1/auth/password/forgot", "", obj{"email": a.Email})
	m, ok := h.mail.last("password_reset", a.Email)
	if !ok || !strings.Contains(m.Link, "/reset-password?token=") {
		t.Fatalf("reset mail: %+v", m)
	}

	newPw := "Gelb-Wolke-Stein-Fluss-77"
	h.wantCode(422, "validation_failed", "POST", "/v1/auth/password/reset", "", obj{"token": m.Token, "new_password": "kurz"})
	h.want(204, "POST", "/v1/auth/password/reset", "", obj{"token": m.Token, "new_password": newPw})
	h.wantCode(400, "invalid_token", "POST", "/v1/auth/password/reset", "", obj{"token": m.Token, "new_password": newPw}) // single use

	// All sessions ended, the old password is dead, and MFA is still required.
	h.wantCode(401, "unauthorized", "GET", "/v1/me", old, nil)
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
	a.Password = newPw
	if st, out := h.login(a, ""); st != 401 {
		t.Fatalf("login without MFA code after a reset: %d %v", st, out)
	}
	if st, _ := h.login(a, h.code(a)); st != 200 {
		t.Fatal("login with the new password and a code must work")
	}
}

func TestPasswordResetLinkExpires(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	h.want(202, "POST", "/v1/auth/password/forgot", "", obj{"email": a.Email})
	m, _ := h.mail.last("password_reset", a.Email)
	// The link lives 30 minutes (database time). Check the stored expiry instead of waiting.
	var exp time.Time
	if err := queryRow(t, `SELECT expires_at FROM email_tokens WHERE user_id = $1 AND purpose = 'reset' AND used_at IS NULL`, a.ID).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 25*time.Minute || d > 31*time.Minute {
		t.Fatalf("reset link lifetime %v, want about 30 minutes", d)
	}
	if len(m.Token) < 40 {
		t.Fatalf("token too short: %q", m.Token)
	}
	// Only the hash is stored.
	var n int
	if err := queryRow(t, `SELECT count(*) FROM email_tokens WHERE token_hash = $1`, m.Token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the raw token must not be stored (n=%d err=%v)", n, err)
	}
}

func TestChangePassword(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)
	newPw := "Gelb-Wolke-Stein-Fluss-77"

	h.wantCode(422, "invalid_current_password", "POST", "/v1/me/password", token, obj{"current_password": "Falsch-Falsch-Falsch-1", "new_password": newPw})
	h.wantCode(422, "validation_failed", "POST", "/v1/me/password", token, obj{"current_password": a.Password, "new_password": "kurz"})
	h.wantCode(422, "validation_failed", "POST", "/v1/me/password", token, obj{"current_password": a.Password, "new_password": a.Password})

	out := h.want(200, "POST", "/v1/me/password", token, obj{"current_password": a.Password, "new_password": newPw})
	if out["scope"] != "full" {
		t.Fatalf("new token: %v", out)
	}
	h.wantCode(401, "unauthorized", "GET", "/v1/me", token, nil) // the old token is dead
	h.want(200, "GET", "/v1/me", str(out, "access_token"), nil)  // the new one works at once
	a.Password = newPw
	if st, _ := h.login(a, h.code(a)); st != 200 {
		t.Fatal("login with the new password")
	}
}

func TestEmailChange(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	other := h.makeUser(false, "patient")
	token := h.token(a)
	newMail := uniqueEmail()

	h.wantCode(422, "invalid_current_password", "POST", "/v1/me/email", token, obj{"new_email": newMail, "current_password": "Falsch-Falsch-Falsch-1"})
	h.wantCode(422, "validation_failed", "POST", "/v1/me/email", token, obj{"new_email": a.Email, "current_password": a.Password})

	// A taken address looks the same and sends nothing.
	before := h.mail.count()
	h.want(202, "POST", "/v1/me/email", token, obj{"new_email": other.Email, "current_password": a.Password})
	if h.mail.count() != before {
		t.Fatal("no mail for a taken address")
	}

	h.want(202, "POST", "/v1/me/email", token, obj{"new_email": newMail, "current_password": a.Password})
	m, ok := h.mail.last("email_change_confirm", newMail)
	if !ok {
		t.Fatal("confirmation link missing")
	}
	if _, ok := h.mail.last("email_change_notice", a.Email); !ok {
		t.Fatal("the old address must get a notice")
	}
	h.want(204, "POST", "/v1/auth/email-change/confirm", "", obj{"token": m.Token})
	h.wantCode(400, "invalid_token", "POST", "/v1/auth/email-change/confirm", "", obj{"token": m.Token})

	old := a.Email
	a.Email = newMail
	if st, _ := h.login(a, h.code(a)); st != 200 {
		t.Fatal("login with the new address")
	}
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": old, "password": a.Password})
}

func TestProfileUpdate(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)
	me := h.want(200, "PATCH", "/v1/me", token, obj{"first_name": "Neu", "phone": "0621123", "address": obj{"city": "Mannheim"}})
	if me["first_name"] != "Neu" || me["phone"] != "0621123" || me["address"].(obj)["city"] != "Mannheim" || me["last_name"] != "Person" {
		t.Fatalf("profile: %v", me)
	}
	h.wantCode(422, "validation_failed", "PATCH", "/v1/me", token, obj{"email": "x@example.org"}) // e-mail has its own route
	h.wantCode(422, "validation_failed", "PATCH", "/v1/me", token, obj{})                         // at least one property

	doctor := h.makeUser(true, "doctor")
	h.wantCode(422, "not_a_patient", "PATCH", "/v1/me", h.token(doctor), obj{"address": obj{"city": "X"}})
}

// UF-13: delete the own account.
func TestDeleteOwnAccount(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	token := h.token(a)

	h.wantCode(422, "invalid_credentials", "POST", "/v1/me/deletion", token, obj{"password": a.Password, "code": "000000"})
	h.wantCode(422, "invalid_credentials", "POST", "/v1/me/deletion", token, obj{"password": "Falsch-Falsch-Falsch-1", "code": h.code(a)})
	h.jumpTo(h.lockedUntil2(a))
	h.want(204, "POST", "/v1/me/deletion", token, obj{"password": a.Password, "code": h.code(a)})
	if _, ok := h.mail.last("account_deleted", a.Email); !ok {
		t.Fatal("confirmation mail missing")
	}
	h.wantCode(401, "unauthorized", "GET", "/v1/me", token, nil)
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
}
