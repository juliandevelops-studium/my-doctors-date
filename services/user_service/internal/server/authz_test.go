package server_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/policy"
)

type routeKey struct{ method, path string }

// specRoutes maps every route of the OpenAPI spec to its contract name (operationId as in authz.yaml).
func specRoutes(t *testing.T) map[routeKey]string {
	t.Helper()
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	out := map[routeKey]string{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			out[routeKey{method, path}] = strings.ToLower(op.OperationID[:1]) + op.OperationID[1:]
		}
	}
	return out
}

// SR.03 review criterion: every registered route is in the authorisation table and the other way round.
func TestRegisteredRoutesMatchAuthz(t *testing.T) {
	h := newHarness(t)
	handler, err := h.srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	spec := specRoutes(t)
	registered := map[routeKey]bool{}
	err = chi.Walk(handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[routeKey{method, route}] = true
		if op, ok := spec[routeKey{method, route}]; !ok {
			t.Errorf("%s %s is registered but not in the spec", method, route)
		} else if _, ok := policy.Routes[op]; !ok {
			t.Errorf("%s %s (%s) has no entry in the authorisation table", method, route, op)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, op := range spec {
		if !registered[k] {
			t.Errorf("%s %s (%s) is in the spec but not registered", k.method, k.path, op)
		}
	}
	names := map[string]bool{}
	for _, op := range spec {
		names[op] = true
	}
	for op := range policy.Routes {
		if !names[op] {
			t.Errorf("policy entry %s has no route", op)
		}
	}
}

func concretePath(path string) string {
	for strings.Contains(path, "{") {
		i, j := strings.Index(path, "{"), strings.Index(path, "}")
		path = path[:i] + uuid.NewString() + path[j+1:]
	}
	return path
}

// SR.03: deny by default, 401 without a valid token, 403 for the wrong role. Checked for every protected route.
func TestRoleMatrixOnEveryRoute(t *testing.T) {
	h := newHarness(t)
	roles := []string{"patient", "doctor", "desk", "organisator", "systemadmin"}
	tokens := map[string]string{}
	for _, r := range roles {
		tokens[r] = h.token(h.makeUser(true, mwRole(r)))
	}
	// A setup-scope token of a user without MFA.
	setup := func() string {
		a := h.makeUser(false, "patient")
		_, out := h.login(a, "")
		return str(out, "access_token")
	}()

	spec := specRoutes(t)
	var ops []string
	byOp := map[string]routeKey{}
	for k, op := range spec {
		byOp[op] = k
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { // logout revokes the token, so it goes last
		if (ops[i] == "logout") != (ops[j] == "logout") {
			return ops[j] == "logout"
		}
		return ops[i] < ops[j]
	})

	checked := 0
	for _, op := range ops {
		rule := policy.Routes[op]
		if rule.Access != policy.User {
			continue
		}
		k := byOp[op]
		path := concretePath(k.path)
		call := func(token string) int { st, _ := h.do(k.method, path, token, obj{}); return st }

		if st := call(""); st != 401 {
			t.Errorf("%s without token: %d, want 401", op, st)
		}
		if st := call("not.a.token"); st != 401 {
			t.Errorf("%s with garbage token: %d, want 401", op, st)
		}
		if !rule.Setup {
			if st := call(setup); st != 401 {
				t.Errorf("%s with a setup token: %d, want 401", op, st)
			}
		} else if st := call(setup); st == 401 || st == 403 {
			t.Errorf("%s must accept a setup token, got %d", op, st)
		}
		for _, r := range roles {
			st := call(tokens[r])
			allowedRole := allowedFor(rule.Roles, r)
			switch {
			case allowedRole && (st == 401 || st == 403):
				t.Errorf("%s as %s: %d, role is allowed", op, r, st)
			case !allowedRole && st != 403:
				t.Errorf("%s as %s: %d, want 403", op, r, st)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d protected routes checked", checked)
	}
}

func allowedFor(want []string, role string) bool {
	for _, w := range want {
		if w == "any" || w == role {
			return true
		}
	}
	return false
}

// Forged and manipulated tokens never pass.
func TestForgedTokens(t *testing.T) {
	h := newHarness(t)
	a := h.makeUser(true, "patient")
	good := h.token(a)
	h.want(200, "GET", "/v1/me", good, nil)

	// Same claims, signed by another key (with the right key id).
	_, otherKey := newKey(t)
	c := claimsOf(t, h, good)
	forged := praxisauth.SignUser(otherKey, praxisauth.KeyID(h.pubKey), *c)
	h.wantCode(401, "unauthorized", "GET", "/v1/me", forged, nil)

	// Valid signature but escalated role: the signature covers the claims, so the original cannot be changed; a token
	// the service signed itself for an admin role of another account is still checked against the account state.
	parts := strings.Split(good, ".")
	h.wantCode(401, "unauthorized", "GET", "/v1/me", parts[0]+".eyJzdWIiOiJ4Iiwicm9sZXMiOlsic3lzdGVtYWRtaW4iXX0."+parts[2], nil)
	h.wantCode(401, "unauthorized", "GET", "/v1/me", "eyJhbGciOiJub25lIiwia2lkIjoieCJ9.e30.", nil)

	// Expired, wrong audience, claims for another subject.
	old := *c
	old.IssuedAt -= 7200
	old.Expires -= 7200
	h.wantCode(401, "unauthorized", "GET", "/v1/me", praxisauth.SignUser(h.jwtKey, praxisauth.KeyID(h.pubKey), old), nil)
	aud := *c
	aud.Audience = "mail_service"
	h.wantCode(401, "unauthorized", "GET", "/v1/me", praxisauth.SignUser(h.jwtKey, praxisauth.KeyID(h.pubKey), aud), nil)
	ghost := *c
	ghost.Subject = uuid.NewString()
	ghost.JTI = uuid.NewString()
	h.wantCode(401, "unauthorized", "GET", "/v1/me", praxisauth.SignUser(h.jwtKey, praxisauth.KeyID(h.pubKey), ghost), nil)

	// A token with mfa=false and scope=full is not accepted.
	nomfa := *c
	nomfa.MFA = false
	nomfa.JTI = uuid.NewString()
	h.wantCode(401, "unauthorized", "GET", "/v1/me", praxisauth.SignUser(h.jwtKey, praxisauth.KeyID(h.pubKey), nomfa), nil)
	_ = time.Now
}

// The identity comes from the token only: a manipulated id in the body or the path has no effect on /me routes.
func TestIdentityOnlyFromToken(t *testing.T) {
	h := newHarness(t)
	victim := h.makeUser(true, "patient")
	attacker := h.makeUser(true, "patient")
	token := h.token(attacker)

	h.wantCode(422, "validation_failed", "PATCH", "/v1/me", token, obj{"id": victim.ID.String(), "first_name": "Evil"})
	me := h.want(200, "GET", "/v1/me", token, nil)
	if me["id"] != attacker.ID.String() {
		t.Fatalf("me: %v", me)
	}
	h.wantCode(403, "forbidden", "GET", "/v1/admin/users/"+victim.ID.String(), token, nil)
}
