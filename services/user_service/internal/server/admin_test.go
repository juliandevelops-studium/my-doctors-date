package server_test

import (
	"context"
	"testing"
)

// UF-10: the administrator creates a user; the user sets the own password through the mailed link.
func TestAdminCreatesUser(t *testing.T) {
	h := newHarness(t)
	admin := h.makeUser(true, "systemadmin")
	token := h.token(admin)
	email := uniqueEmail()

	h.wantCode(403, "forbidden", "POST", "/v1/admin/users", h.token(h.makeUser(true, "organisator")), obj{"first_name": "A", "last_name": "B", "email": email, "roles": []string{"doctor"}})

	_, created, hdr := h.req("POST", "/v1/admin/users", token, obj{"first_name": "Dora", "last_name": "Doktor", "email": email, "roles": []string{"doctor"}})
	id := str(created, "id")
	if id == "" || hdr.Get("Location") != "/v1/admin/users/"+id || created["status"] != "active" {
		t.Fatalf("created user: %v %q", created, hdr.Get("Location"))
	}
	h.wantCode(409, "email_taken", "POST", "/v1/admin/users", token, obj{"first_name": "A", "last_name": "B", "email": email, "roles": []string{"doctor"}})
	h.wantCode(422, "systemadmin_exclusive", "POST", "/v1/admin/users", token, obj{"first_name": "A", "last_name": "B", "email": uniqueEmail(), "roles": []string{"systemadmin", "doctor"}})

	// The user has no usable password until the link is used. The administrator never sees a password.
	m, ok := h.mail.last("password_reset", email)
	if !ok {
		t.Fatal("reset mail missing")
	}
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": email, "password": goodPassword})
	h.want(204, "POST", "/v1/auth/password/reset", "", obj{"token": m.Token, "new_password": goodPassword})
	a := &account{Email: email, Password: goodPassword}
	st, out := h.login(a, "")
	if st != 200 || out["scope"] != "setup" {
		t.Fatalf("first login of the new user: %d %v", st, out)
	}
	if got := h.want(200, "GET", "/v1/me", str(out, "access_token"), nil); got["roles"].([]any)[0] != "doctor" {
		t.Fatalf("roles: %v", got["roles"])
	}
}

func TestAdminRoleManagement(t *testing.T) {
	h := newHarness(t)
	admin := h.makeUser(true, "systemadmin")
	token := h.token(admin)
	target := h.makeUser(true, "patient")
	targetToken := h.token(target)
	path := "/v1/admin/users/" + target.ID.String() + "/roles"

	h.wantCode(422, "systemadmin_exclusive", "PUT", path, token, obj{"roles": []string{"systemadmin", "patient"}})
	h.wantCode(422, "own_roles", "PUT", "/v1/admin/users/"+admin.ID.String()+"/roles", token, obj{"roles": []string{"patient"}})
	h.wantCode(404, "not_found", "PUT", "/v1/admin/users/00000000-0000-0000-0000-000000000042/roles", token, obj{"roles": []string{"patient"}})
	h.wantCode(422, "validation_failed", "PUT", path, token, obj{"roles": []string{"god"}})

	got := h.want(200, "PUT", path, token, obj{"roles": []string{"patient", "doctor"}})
	if len(got["roles"].([]any)) != 2 {
		t.Fatalf("roles: %v", got["roles"])
	}
	// The roles are part of the token, so the old sessions end (decision 13).
	h.wantCode(401, "unauthorized", "GET", "/v1/me", targetToken, nil)
	if st, _ := h.login(target, h.code(target)); st != 200 {
		t.Fatal("the user must be able to log in again")
	}
}

func TestAdminResetUnlockDeleteAndAudit(t *testing.T) {
	h := newHarness(t)
	admin := h.makeUser(true, "systemadmin")
	token := h.token(admin)
	target := h.makeUser(true, "desk")
	targetToken := h.token(target)
	base := "/v1/admin/users/" + target.ID.String()

	got := h.want(200, "GET", base, token, nil)
	if got["email"] != target.Email || got["mfa_enabled"] != true {
		t.Fatalf("user: %v", got)
	}
	h.wantCode(404, "not_found", "GET", "/v1/admin/users/00000000-0000-0000-0000-000000000042", token, nil)

	list := h.want(200, "GET", "/v1/admin/users?q="+target.Email, token, nil)
	if list["total"].(float64) != 1 {
		t.Fatalf("search: %v", list)
	}
	if st, _ := h.do("GET", "/v1/admin/users?limit=500", token, nil); st != 422 {
		t.Fatalf("limit above 200: %d", st)
	}

	// Password reset by the administrator: link by mail, the sessions of the user end.
	h.want(202, "POST", base+"/password-reset", token, nil)
	if _, ok := h.mail.last("password_reset", target.Email); !ok {
		t.Fatal("reset mail missing")
	}
	h.wantCode(401, "unauthorized", "GET", "/v1/me", targetToken, nil)

	h.want(204, "POST", base+"/unlock", token, nil)
	h.wantCode(404, "not_found", "POST", "/v1/admin/users/00000000-0000-0000-0000-000000000042/unlock", token, nil)

	// Deletion.
	h.wantCode(422, "own_account", "DELETE", "/v1/admin/users/"+admin.ID.String(), token, nil)
	h.want(204, "DELETE", base, token, nil)
	h.wantCode(404, "not_found", "GET", base, token, nil)
	h.wantCode(401, "invalid_credentials", "POST", "/v1/auth/login", "", obj{"email": target.Email, "password": target.Password})

	// Audit: the actions are recorded with actor and target, without any personal content.
	for _, action := range []string{"password_reset_requested", "user_unlocked", "account_deleted"} {
		page := h.want(200, "GET", "/v1/admin/audit-log?action="+action+"&limit=200", token, nil)
		found := false
		for _, e := range page["items"].([]any) {
			if e.(obj)["target_user_id"] == target.ID.String() && e.(obj)["actor_user_id"] == admin.ID.String() {
				found = true
			}
		}
		if !found {
			t.Errorf("audit entry %s missing", action)
		}
	}
	h.wantCode(403, "forbidden", "GET", "/v1/admin/audit-log", h.token(h.makeUser(true, "organisator")), nil)
}

func TestAdminRetention(t *testing.T) {
	h := newHarness(t)
	token := h.token(h.makeUser(true, "systemadmin"))
	cur := h.want(200, "GET", "/v1/admin/retention", token, nil)
	if cur["retention_a_days"] == nil {
		t.Fatalf("retention: %v", cur)
	}
	set := func(b, c int) (int, obj) {
		return h.do("PUT", "/v1/admin/retention", token, obj{"retention_a_days": 30, "retention_b_days": b, "retention_c_days": c, "retention_d_days": 30, "retention_e_days": 90})
	}
	if st, out := set(400, 300); st != 422 || out["code"] != "validation_failed" {
		t.Fatalf("C < B: %d %v", st, out)
	}
	if st, out := set(365, 730); st != 200 || out["retention_c_days"].(float64) != 730 {
		t.Fatalf("valid: %d %v", st, out)
	}
	if st, _ := h.do("PUT", "/v1/admin/retention", token, obj{"retention_a_days": 0, "retention_b_days": 1, "retention_c_days": 1, "retention_d_days": 1, "retention_e_days": 1}); st != 422 {
		t.Fatalf("zero days: %d", st)
	}
	h.wantCode(403, "forbidden", "GET", "/v1/admin/retention", h.token(h.makeUser(true, "desk")), nil)
}

func TestJwksAndBootstrap(t *testing.T) {
	h := newHarness(t)
	jwks := h.want(200, "GET", "/v1/jwks", "", nil)
	keys := jwks["keys"].([]any)
	if len(keys) != 1 || keys[0].(obj)["kty"] != "OKP" || keys[0].(obj)["crv"] != "Ed25519" || keys[0].(obj)["alg"] != "EdDSA" {
		t.Fatalf("jwks: %v", jwks)
	}
	h.makeUser(true, "systemadmin") // an administrator exists, so the bootstrap does nothing
	created, err := h.srv.EnsureBootstrapAdmin(context.Background(), uniqueEmail(), goodPassword)
	if err != nil || created {
		t.Fatalf("bootstrap with an existing administrator: %v %v", created, err)
	}
	h.want(200, "GET", "/healthz", "", nil)
}
