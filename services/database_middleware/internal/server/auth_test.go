package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"user_id":"7f8f1f0e-4f43-4b56-9f1c-2f6f2b0f2a12"}`)

	for name, tok := range map[string]string{"no token": "", "garbage": "abc.def.ghi"} {
		if got, _ := e.raw(tok, "userGetById", "application/json", body); got != 401 {
			t.Errorf("%s: status %d, want 401", name, got)
		}
	}
	if got, _ := e.raw(e.token("user_service"), "userGetById", "application/json", body); got != 404 {
		t.Errorf("unknown user: status %d, want 404", got)
	}
}

// The kubelet probes /healthz without a token.
func TestHealthzNeedsNoToken(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/healthz: %d, want 200", resp.StatusCode)
	}
}

func TestAllowListIsEnforced(t *testing.T) {
	e := newEnv(t)
	id := "7f8f1f0e-4f43-4b56-9f1c-2f6f2b0f2a12"
	cases := []struct {
		caller, op string
		body       obj
	}{
		{"calendar_service", "userGetByEmail", obj{"email": "a@example.org"}}, // SR.02: no password hashes for calendar
		{"calendar_service", "mfaSecretGet", obj{"user_id": id}},
		{"calendar_service", "userCreate", obj{}},
		{"user_service", "bookingList", obj{}}, // user_service must not read bookings
		{"user_service", "bookingGet", obj{"booking_id": id}},
		{"user_service", "serviceList", nil},
		{"purge_job", "userList", obj{}},
		{"purge_job", "bookingCreate", obj{}},
		{"user_service", "purgeExpired", obj{"category": "tokens"}},
		{"calendar_service", "purgeExpired", obj{"category": "tokens"}},
		{"calendar_service", "bookingsCompletePast", nil},
		{"mail_service", "tokenCheck", obj{}}, // mail_service has no operation at all
		{"mail_service", "serviceList", nil},
	}
	for _, c := range cases {
		e.expect(c.caller, c.op, c.body, 403, "forbidden")
	}
	e.expect("user_service", "doesNotExist", obj{}, 404, "not_found")
}

func TestRequestValidation(t *testing.T) {
	e := newEnv(t)
	good := `{"user_id":"7f8f1f0e-4f43-4b56-9f1c-2f6f2b0f2a12"}`

	if got, _ := e.raw(e.token("user_service"), "userGetById", "application/json", []byte(`{"user_id":"7f8f1f0e-4f43-4b56-9f1c-2f6f2b0f2a12","extra":1}`)); got != 422 {
		t.Errorf("unknown field: status %d, want 422", got)
	}
	if got, _ := e.raw(e.token("user_service"), "userGetById", "application/json", []byte(`{"user_id":"not-a-uuid"}`)); got != 400 && got != 422 {
		t.Errorf("bad uuid: status %d, want 400 or 422", got)
	}
	if got, _ := e.raw(e.token("user_service"), "userGetById", "application/json", []byte(`{`)); got != 400 && got != 422 {
		t.Errorf("broken json: status %d", got)
	}
	if got, _ := e.raw(e.token("user_service"), "userGetById", "text/plain", []byte(good)); got != 415 {
		t.Errorf("wrong content type: status %d, want 415", got)
	}
	big := `{"email":"` + strings.Repeat("a", 70<<10) + `@example.org"}`
	if got, _ := e.raw(e.token("user_service"), "userGetByEmail", "application/json", []byte(big)); got != 413 && got != 422 {
		t.Errorf("oversized body: status %d, want 413 or 422", got)
	}
	// SR.01: only Argon2id hashes are accepted, checked by the schema before the database.
	e.expect("user_service", "userCreate", obj{
		"first_name": "A", "last_name": "B", "email": "plain@example.org", "password_hash": "plaintext",
		"roles": []string{"patient"}, "status": "active", "must_change_password": false,
	}, 422, "validation_failed")
}
