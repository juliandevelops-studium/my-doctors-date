package server_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAccountCreateAndLookup(t *testing.T) {
	e := newEnv(t)
	id, email := e.newUser("active", "patient")

	got := e.ok("user_service", "userGetByEmail", obj{"email": "  "[:0] + upper(email)}, 200)
	if str(got, "id") != id || str(got, "password_hash") != hash {
		t.Fatalf("case-insensitive lookup failed: %v", got)
	}
	if got["address"] == nil {
		t.Error("a patient must have an address object")
	}
	e.expect("user_service", "userCreate", obj{
		"first_name": "A", "last_name": "B", "email": email, "password_hash": hash,
		"roles": []string{"patient"}, "status": "active", "must_change_password": false,
	}, 409, "duplicate")
	// Decision 9: systemadmin is exclusive.
	e.expect("user_service", "userCreate", obj{
		"first_name": "A", "last_name": "B", "email": "x-" + email, "password_hash": hash,
		"roles": []string{"systemadmin", "patient"}, "status": "active", "must_change_password": false,
	}, 422, "systemadmin_exclusive")
	e.expect("user_service", "userSetRoles", obj{"user_id": id, "roles": []string{"systemadmin", "doctor"}}, 422, "systemadmin_exclusive")
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestRolesKeepDoctorRowsInSync(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	if hasDoctor(e.ok("calendar_service", "doctorList", obj{}, 200), id) {
		t.Fatal("a patient must not be listed as doctor")
	}
	e.ok("user_service", "userSetRoles", obj{"user_id": id, "roles": []string{"patient", "doctor"}}, 200)
	if !hasDoctor(e.ok("calendar_service", "doctorList", obj{}, 200), id) {
		t.Fatal("the new doctor must be listed")
	}
	e.ok("user_service", "userSetRoles", obj{"user_id": id, "roles": []string{"patient"}}, 200)
	if hasDoctor(e.ok("calendar_service", "doctorList", obj{}, 200), id) {
		t.Fatal("a user who lost the role doctor must not be listed")
	}
}

func hasDoctor(list0 obj, id string) bool {
	for _, d := range list(list0, "items") {
		if str(d.(obj), "id") == id {
			return true
		}
	}
	return false
}

func TestSessionsAreInvalidatedByPasswordChange(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	check := func(iat time.Time, jti string) bool {
		out := e.ok("calendar_service", "tokenCheck", obj{"jti": jti, "sub": id, "iat": ts(iat)}, 200)
		return out["valid"] == true
	}
	jti := uuid.NewString()
	if !check(time.Now(), jti) {
		t.Fatal("a fresh token must be valid")
	}
	// The change takes effect at the next full second: even a token issued in the same second is dead (decision 20).
	oldToken := time.Now()
	e.ok("user_service", "userSetPassword", obj{"user_id": id, "password_hash": hash, "must_change_password": false}, 204)
	if check(oldToken, uuid.NewString()) {
		t.Fatal("a token issued before the password change must be invalid")
	}
	// user_service stamps new tokens with max(now, sessions_valid_after), which is valid at once.
	after, err := time.Parse(time.RFC3339Nano, str(e.ok("user_service", "userGetById", obj{"user_id": id}, 200), "sessions_valid_after"))
	if err != nil {
		t.Fatal(err)
	}
	if !check(after, uuid.NewString()) {
		t.Fatal("a token stamped with sessions_valid_after must be valid")
	}
	// Revocation of one jti (logout).
	jti = uuid.NewString()
	e.ok("user_service", "tokenRevoke", obj{"jti": jti, "user_id": id, "expires_at": ts(time.Now().Add(time.Hour))}, 204)
	if check(time.Now(), jti) {
		t.Fatal("a revoked jti must be invalid")
	}
	// Pending and unknown accounts never validate.
	pending, _ := e.newUser("pending", "patient")
	if out := e.ok("user_service", "tokenCheck", obj{"jti": uuid.NewString(), "sub": pending, "iat": ts(time.Now())}, 200); out["valid"] == true {
		t.Fatal("a pending account must not validate")
	}
}

// SR.05: 3rd failure -> 1 minute, each further failure -> 10 minutes, 10th failure -> permanent. Only unlock lifts it.
func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	fail := func() obj { return e.ok("user_service", "userRecordLoginFailure", obj{"user_id": id}, 200) }

	for i := 1; i <= 2; i++ {
		if out := fail(); out["locked_until"] != nil || out["failed_attempts"].(float64) != float64(i) {
			t.Fatalf("attempt %d must not lock: %v", i, out)
		}
	}
	d := lockDuration(t, fail())
	if d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("3rd failure locks for %v, want about 1 minute", d)
	}
	for i := 4; i <= 9; i++ {
		d = lockDuration(t, fail())
		if d < 9*time.Minute || d > 11*time.Minute {
			t.Fatalf("attempt %d locks for %v, want about 10 minutes", i, d)
		}
	}
	out := fail()
	if out["permanently_locked"] != true {
		t.Fatalf("10th failure must lock permanently: %v", out)
	}
	if got := e.ok("user_service", "userGetById", obj{"user_id": id}, 200); got["status"] != "locked" {
		t.Fatalf("status = %v, want locked", got["status"])
	}
	if valid := e.ok("calendar_service", "tokenCheck", obj{"jti": uuid.NewString(), "sub": id, "iat": ts(time.Now())}, 200)["valid"]; valid == true {
		t.Fatal("a locked account must not validate tokens")
	}
	// A success never lifts a permanent lock.
	e.ok("user_service", "userRecordLoginSuccess", obj{"user_id": id}, 404)

	e.ok("user_service", "userUnlock", obj{"user_id": id}, 204)
	got := e.ok("user_service", "userGetById", obj{"user_id": id}, 200)
	if got["status"] != "active" || got["permanently_locked"] != false || got["failed_attempts"].(float64) != 0 || got["locked_until"] != nil {
		t.Fatalf("unlock incomplete: %v", got)
	}
}

func lockDuration(t *testing.T, st obj) time.Duration {
	t.Helper()
	s := str(st, "locked_until")
	if s == "" {
		t.Fatalf("expected a lock: %v", st)
	}
	until, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return time.Until(until)
}

func TestEmailTokens(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("pending", "patient")
	mk := func(purpose, hashv string, ttl time.Duration) {
		e.ok("user_service", "emailTokenCreate", obj{"user_id": id, "purpose": purpose, "token_hash": hashv, "expires_at": ts(time.Now().Add(ttl))}, 204)
	}
	h1, h2, h3 := uuid.NewString()+uuid.NewString(), uuid.NewString()+uuid.NewString(), uuid.NewString()+uuid.NewString()

	mk("verify", h1, time.Hour)
	out := e.ok("user_service", "emailTokenConsume", obj{"purpose": "verify", "token_hash": h1}, 200)
	if str(out, "user_id") != id {
		t.Fatalf("wrong user: %v", out)
	}
	e.expect("user_service", "emailTokenConsume", obj{"purpose": "verify", "token_hash": h1}, 404, "not_found") // single use
	mk("reset", h2, time.Hour)
	e.expect("user_service", "emailTokenConsume", obj{"purpose": "verify", "token_hash": h2}, 404, "not_found") // wrong purpose
	mk("reset", h3, time.Hour)                                                                                  // supersedes h2
	e.expect("user_service", "emailTokenConsume", obj{"purpose": "reset", "token_hash": h2}, 404, "not_found")
	e.ok("user_service", "emailTokenConsume", obj{"purpose": "reset", "token_hash": h3}, 200)
	h4 := uuid.NewString() + uuid.NewString()
	mk("verify", h4, -time.Minute)
	e.expect("user_service", "emailTokenConsume", obj{"purpose": "verify", "token_hash": h4}, 404, "not_found") // expired

	// e-mail change carries the new address
	h5 := uuid.NewString() + uuid.NewString()
	newMail := "new-" + uuid.NewString() + "@example.org"
	e.ok("user_service", "emailTokenCreate", obj{"user_id": id, "purpose": "email_change", "token_hash": h5, "new_email": newMail, "expires_at": ts(time.Now().Add(time.Hour))}, 204)
	out = e.ok("user_service", "emailTokenConsume", obj{"purpose": "email_change", "token_hash": h5}, 200)
	if str(out, "new_email") != newMail {
		t.Fatalf("new_email = %v", out["new_email"])
	}
	e.ok("user_service", "userChangeEmail", obj{"user_id": id, "new_email": newMail}, 204)
	e.ok("user_service", "userGetByEmail", obj{"email": newMail}, 200)
	// new_email is only allowed for email_change
	e.expect("user_service", "emailTokenCreate", obj{"user_id": id, "purpose": "verify", "token_hash": uuid.NewString() + uuid.NewString(), "new_email": newMail, "expires_at": ts(time.Now().Add(time.Hour))}, 422, "check_violation")
}

// Decision 22: a confirmed secret is never overwritten. MFA has to be removed first, then set up again.
func TestMfaCanOnlyBeReplacedAfterRemoval(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	e.expect("user_service", "mfaSecretGet", obj{"user_id": id}, 404, "not_found")
	e.expect("user_service", "mfaSecretConfirm", obj{"user_id": id}, 404, "not_found") // nothing to confirm

	e.ok("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "c2VjcmV0LW9uZQ=="}, 204)
	e.ok("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "c2VjcmV0LW9uZXg="}, 204) // an unfinished setup may be restarted
	got := e.ok("user_service", "mfaSecretGet", obj{"user_id": id}, 200)
	if got["confirmed"] != false || got["secret_enc"] != "c2VjcmV0LW9uZXg=" {
		t.Fatalf("unfinished setup: %v", got)
	}
	if e.ok("user_service", "userGetById", obj{"user_id": id}, 200)["mfa_enabled"] != false {
		t.Fatal("MFA must not be enabled before confirmation")
	}

	e.ok("user_service", "mfaSecretConfirm", obj{"user_id": id}, 204)
	if e.ok("user_service", "userGetById", obj{"user_id": id}, 200)["mfa_enabled"] != true {
		t.Fatal("MFA must be enabled after confirmation")
	}
	e.expect("user_service", "mfaSecretConfirm", obj{"user_id": id}, 404, "not_found") // already confirmed

	e.expect("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "YXR0YWNrZXI="}, 409, "mfa_active")
	got = e.ok("user_service", "mfaSecretGet", obj{"user_id": id}, 200)
	if got["confirmed"] != true || got["secret_enc"] != "c2VjcmV0LW9uZXg=" {
		t.Fatalf("a confirmed secret must survive a setup attempt: %v", got)
	}

	e.ok("user_service", "mfaDelete", obj{"user_id": id}, 204)
	e.expect("user_service", "mfaSecretGet", obj{"user_id": id}, 404, "not_found")
	e.ok("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "bmV3LXNlY3JldA=="}, 204)
	e.ok("user_service", "mfaSecretConfirm", obj{"user_id": id}, 204)
}

// RFC 6238: a time step is accepted once; older steps are never accepted again.
func TestMfaStepReplayProtection(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	e.expect("user_service", "mfaUseStep", obj{"user_id": id, "step": 100}, 404, "not_found") // no MFA yet
	e.ok("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "c2VjcmV0"}, 204)

	use := func(step int) bool {
		return e.ok("user_service", "mfaUseStep", obj{"user_id": id, "step": step}, 200)["accepted"] == true
	}
	if !use(100) {
		t.Fatal("first use of a step must be accepted")
	}
	if use(100) {
		t.Fatal("the same step must not be accepted twice")
	}
	if use(99) {
		t.Fatal("an older step must not be accepted after a newer one")
	}
	if !use(101) {
		t.Fatal("a newer step must be accepted")
	}
	e.expect("calendar_service", "mfaUseStep", obj{"user_id": id, "step": 102}, 403, "forbidden")
}

func TestRecoveryCodesAreSingleUse(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	hashes := make([]string, 10)
	for i := range hashes {
		hashes[i] = uuid.NewString() + uuid.NewString()
	}
	e.ok("user_service", "recoveryCodesReplace", obj{"user_id": id, "code_hashes": hashes}, 204)
	if e.ok("user_service", "recoveryCodeConsume", obj{"user_id": id, "code_hash": hashes[0]}, 200)["consumed"] != true {
		t.Fatal("first use must succeed")
	}
	if e.ok("user_service", "recoveryCodeConsume", obj{"user_id": id, "code_hash": hashes[0]}, 200)["consumed"] != false {
		t.Fatal("second use must fail")
	}
	e.ok("user_service", "recoveryCodesReplace", obj{"user_id": id, "code_hashes": hashes}, 204)
	if e.ok("user_service", "recoveryCodeConsume", obj{"user_id": id, "code_hash": hashes[0]}, 200)["consumed"] != true {
		t.Fatal("replace must issue fresh codes")
	}
}

func TestProfileUpdate(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser("active", "patient")
	got := e.ok("user_service", "userUpdateProfile", obj{"user_id": id, "first_name": "Neu", "phone": "0123", "address": obj{"city": "Mannheim"}}, 200)
	addr := got["address"].(obj)
	if got["first_name"] != "Neu" || got["phone"] != "0123" || addr["city"] != "Mannheim" || got["last_name"] != "User" {
		t.Fatalf("update: %v", got)
	}
	got = e.ok("user_service", "userUpdateProfile", obj{"user_id": id, "phone": ""}, 200)
	if got["phone"] != nil {
		t.Fatalf("an empty string must clear the phone: %v", got["phone"])
	}
	doc := e.newDoctor()
	e.expect("user_service", "userUpdateProfile", obj{"user_id": doc, "address": obj{"city": "X"}}, 422, "not_a_patient")
}
