package server_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func codesOf(out obj) []string {
	var codes []string
	errs, _ := out["errors"].([]any)
	for _, e := range errs {
		codes = append(codes, e.(obj)["code"].(string))
	}
	return codes
}

// UF-01: registration, confirmation by link, neutral answers.
func TestRegistrationFlow(t *testing.T) {
	h := newHarness(t)
	email := uniqueEmail()
	reg := obj{"first_name": "Erika", "last_name": "Muster", "email": email, "password": goodPassword,
		"address": obj{"street": "Hauptstr. 1", "postal_code": "68159", "city": "Mannheim"}}

	h.want(202, "POST", "/v1/auth/register", "", reg)
	m, ok := h.mail.last("verify_email", email)
	if !ok || m.Token == "" || !strings.HasPrefix(m.Link, "https://praxis.test/verify-email?token=") {
		t.Fatalf("verification mail: %+v", m)
	}

	// Before the confirmation the account cannot log in, and the answer is the neutral one.
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": email, "password": goodPassword})

	h.want(204, "POST", "/v1/auth/verify-email", "", obj{"token": m.Token})
	h.wantCode(400, "invalid_token", "POST", "/v1/auth/verify-email", "", obj{"token": m.Token}) // single use
	h.wantCode(400, "invalid_token", "POST", "/v1/auth/verify-email", "", obj{"token": strings.Repeat("A", 43)})
	h.want(200, "POST", "/v1/auth/login", "", obj{"email": email, "password": goodPassword})

	// Registering the same address again looks exactly the same and sends nothing.
	before := h.mail.count()
	reg["email"] = strings.ToUpper(email)
	h.want(202, "POST", "/v1/auth/register", "", reg)
	if h.mail.count() != before {
		t.Fatal("a second registration must not send a mail")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	pending := uniqueEmail()
	h.want(202, "POST", "/v1/auth/register", "", obj{"first_name": "A", "last_name": "B", "email": pending, "password": goodPassword})

	answers := map[string]obj{}
	for name, body := range map[string]obj{
		"unknown e-mail": {"email": uniqueEmail(), "password": goodPassword},
		"wrong password": {"email": a.Email, "password": "Falsches-Passwort-123456"},
		"unconfirmed":    {"email": pending, "password": goodPassword},
	} {
		st, out := h.do("POST", "/v1/auth/login", "", body)
		if st != 401 {
			t.Fatalf("%s: status %d", name, st)
		}
		answers[name] = out
	}
	if !reflect.DeepEqual(answers["unknown e-mail"], answers["wrong password"]) || !reflect.DeepEqual(answers["wrong password"], answers["unconfirmed"]) {
		t.Fatalf("answers differ: %v", answers)
	}
}

// SR.04 at the API: 14 characters rejected, 15 accepted, blocklist, direct request without the frontend.
func TestPasswordPolicyAtTheApi(t *testing.T) {
	h := newHarness(t)
	try := func(pw string) (int, obj) {
		return h.do("POST", "/v1/auth/register", "", obj{"first_name": "A", "last_name": "B", "email": uniqueEmail(), "password": pw})
	}
	if st, out := try("kT7#qLm2vXz9wP"); st != 422 || len(codesOf(out)) != 1 || codesOf(out)[0] != "min_length" {
		t.Errorf("14 characters: %d %v", st, out)
	}
	if st, _ := try("kT7#qLm2vXz9wPa"); st != 202 {
		t.Errorf("15 characters: %d", st)
	}
	if st, _ := try(strings.Repeat("kT7#", 128)); st != 422 { // 512 characters but a repeated block
		t.Errorf("512 repeated characters: %d", st)
	}
	if st, out := try(strings.Repeat("a", 513)); st != 422 || codesOf(out)[0] != "max_length" {
		t.Errorf("513 characters: %d %v", st, out)
	}
	raw, err := os.ReadFile("../password/blocklist.txt")
	if err != nil {
		t.Fatal(err)
	}
	listed := strings.Split(strings.TrimSpace(string(raw)), "\n")[100]
	if st, out := try(listed); st != 422 || codesOf(out)[0] != "password_blocklisted" {
		t.Errorf("blocklisted %q: %d %v", listed, st, out)
	}
}

func TestStrictRequestValidation(t *testing.T) {
	h := newHarness(t)
	h.wantCode(422, "validation_failed", "POST", "/v1/auth/login", "", obj{"email": uniqueEmail(), "password": "x", "admin": true})
	h.wantCode(422, "validation_failed", "POST", "/v1/auth/login", "", obj{"email": "not-an-email", "password": "x"})
	h.wantCode(422, "validation_failed", "POST", "/v1/auth/register", "", obj{"first_name": strings.Repeat("a", 101), "last_name": "B", "email": uniqueEmail(), "password": goodPassword})
	if st, _, _ := h.req("POST", "/v1/auth/login", "", []byte(`{"email":`)); st != 400 && st != 422 {
		t.Errorf("broken JSON: %d", st)
	}
	if st, _, _ := h.req("POST", "/v1/auth/login", "", []byte(`{}`), "Content-Type", "text/plain"); st != 415 {
		t.Errorf("content type: %d", st)
	}
	big := []byte(`{"email":"` + strings.Repeat("a", 70<<10) + `@example.org","password":"x"}`)
	if st, _, _ := h.req("POST", "/v1/auth/login", "", big); st != 413 && st != 422 {
		t.Errorf("oversized body: %d", st)
	}
}

func TestRateLimitOnPublicRoutes(t *testing.T) {
	h := newHarness(t, options{publicPerMinute: 3, userPerMinute: 100, emailPerMinute: 100})
	for i := 0; i < 3; i++ {
		st, _, _ := h.req("POST", "/v1/auth/forgot-password-not-a-route", "", obj{})
		_ = st
	}
	body := obj{"email": uniqueEmail()}
	for i := 0; i < 3; i++ {
		if st, _, _ := h.req("POST", "/v1/auth/password/forgot", "", body, "X-Forwarded-For", "203.0.113.7"); st != 202 {
			t.Fatalf("request %d: %d", i, st)
		}
	}
	st, out, hdr := h.req("POST", "/v1/auth/password/forgot", "", body, "X-Forwarded-For", "203.0.113.7")
	if st != 429 || out["code"] != "rate_limited" || hdr.Get("Retry-After") == "" {
		t.Fatalf("4th request: %d %v %q", st, out, hdr.Get("Retry-After"))
	}
	if st, _, _ := h.req("POST", "/v1/auth/password/forgot", "", body, "X-Forwarded-For", "203.0.113.8"); st != 202 {
		t.Fatalf("another client must not be limited: %d", st)
	}
}
