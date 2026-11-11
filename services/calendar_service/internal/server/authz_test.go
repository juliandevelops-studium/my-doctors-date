package server_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/policy"
)

type routeKey struct{ method, path string }

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
	names := map[string]bool{}
	for k, op := range spec {
		names[op] = true
		if !registered[k] {
			t.Errorf("%s %s (%s) is in the spec but not registered", k.method, k.path, op)
		}
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

func allowedFor(want []string, role string) bool {
	for _, w := range want {
		if w == "any" || w == role {
			return true
		}
	}
	return false
}

// SR.03: deny by default. 401 without a valid token, 403 for the wrong role, on every protected route.
func TestRoleMatrixOnEveryRoute(t *testing.T) {
	h := newHarness(t)
	roles := []string{"patient", "doctor", "desk", "organisator", "systemadmin"}
	tokens := map[string]string{}
	for _, r := range roles {
		tokens[r] = makeUser(t, r, r).tok()
	}
	// A setup-scope token is not accepted anywhere in calendar_service.
	setupUser := makeUser(t, "Setup", "patient")
	setup := praxisauth.SignUser(shared.jwtKey, praxisauth.KeyID(shared.jwtKey.Public().(ed25519.PublicKey)), praxisauth.UserClaims{
		Issuer: praxisauth.UserIssuer, Audience: praxisauth.AudienceAPI, Subject: setupUser.ID, Roles: []string{"patient"},
		IssuedAt: time.Now().Unix(), Expires: time.Now().Add(time.Minute).Unix(), JTI: uuid.NewString(), Scope: praxisauth.ScopeSetup})

	spec := specRoutes(t)
	var ops []string
	byOp := map[string]routeKey{}
	for k, op := range spec {
		byOp[op] = k
		ops = append(ops, op)
	}
	sort.Strings(ops)

	checked := 0
	for _, op := range ops {
		rule := policy.Routes[op]
		if rule.Access != policy.User {
			continue
		}
		k := byOp[op]
		path := concretePath(k.path)
		// Query parameters make the request valid enough to reach the handler for the allowed roles.
		if k.method == "GET" && strings.Contains(path, "calendar") {
			path += "?service_id=" + uuid.NewString() + "&from=2030-01-01T00:00:00Z&to=2030-01-02T00:00:00Z"
		}
		call := func(token string) int { st, _ := h.req(k.method, path, token, obj{}); return st }

		if st := call(""); st != 401 {
			t.Errorf("%s without token: %d, want 401", op, st)
		}
		if st := call("not.a.token"); st != 401 {
			t.Errorf("%s with garbage token: %d, want 401", op, st)
		}
		if st := call(setup); st != 401 {
			t.Errorf("%s with a setup token: %d, want 401", op, st)
		}
		for _, r := range roles {
			st := call(tokens[r])
			ok := allowedFor(rule.Roles, r)
			switch {
			case ok && (st == 401 || st == 403):
				t.Errorf("%s as %s: %d, the role is allowed", op, r, st)
			case !ok && st != 403:
				t.Errorf("%s as %s: %d, want 403", op, r, st)
			}
		}
		checked++
	}
	if checked < 18 {
		t.Fatalf("only %d protected routes checked", checked)
	}
}

// The system administrator has no access to appointment data, whatever the route (matrix last row).
func TestSystemadminCannotReadAppointmentData(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	b := p.mustBook(p.patient("Paula"), p.service, p.at(0, 9, 0), nil)
	admin := makeUser(t, "Admin", "systemadmin")
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/bookings"}, {"GET", "/v1/bookings/" + str(b, "id")}, {"POST", "/v1/bookings/" + str(b, "id") + "/cancel"},
		{"PATCH", "/v1/bookings/" + str(b, "id")}, {"GET", "/v1/patients"}, {"GET", "/v1/doctors"}, {"GET", "/v1/services"},
	} {
		h.wantCode(403, "forbidden", c.method, c.path, admin.tok(), obj{"reason": "x"})
	}
	// The same person with the role organisator in addition is a different matter, but systemadmin is exclusive
	// (decision 9), so the middleware refuses that combination.
	st, _ := mwCall(t, "userCreate", obj{"first_name": "A", "last_name": "B", "email": "x-" + uuid.NewString() + "@example.org",
		"password_hash": "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA", "roles": []string{"systemadmin", "desk"}, "status": "active", "must_change_password": false})
	if st != 422 {
		t.Fatalf("systemadmin + empfang: %d, want 422", st)
	}
}

func TestForgedTokens(t *testing.T) {
	h := newHarness(t)
	u := makeUser(t, "Paula", "patient")
	good := u.tok()
	h.want(200, "GET", "/v1/services", good, nil)

	kid := praxisauth.KeyID(shared.jwtKey.Public().(ed25519.PublicKey))
	c := claimsOf(t, good)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	sign := func(key ed25519.PrivateKey, mod func(*praxisauth.UserClaims)) string {
		cc := *c
		mod(&cc)
		return praxisauth.SignUser(key, kid, cc)
	}
	for name, tok := range map[string]string{
		"signed by another key": sign(otherKey, func(*praxisauth.UserClaims) {}),
		"escalated role":        sign(otherKey, func(c *praxisauth.UserClaims) { c.Roles = []string{"organisator"} }),
		"expired":               sign(shared.jwtKey, func(c *praxisauth.UserClaims) { c.IssuedAt -= 7200; c.Expires -= 7200 }),
		"wrong audience":        sign(shared.jwtKey, func(c *praxisauth.UserClaims) { c.Audience = "mail_service" }),
		"unknown subject":       sign(shared.jwtKey, func(c *praxisauth.UserClaims) { c.Subject = uuid.NewString(); c.JTI = uuid.NewString() }),
		"mfa not verified":      sign(shared.jwtKey, func(c *praxisauth.UserClaims) { c.MFA = false; c.JTI = uuid.NewString() }),
		"challenge as access":   sign(shared.jwtKey, func(c *praxisauth.UserClaims) { c.Audience = praxisauth.AudienceMFA }),
		"alg none":              "eyJhbGciOiJub25lIiwia2lkIjoieCJ9.e30.",
	} {
		if st, _ := h.req("GET", "/v1/services", tok, nil); st != 401 {
			t.Errorf("%s: %d, want 401", name, st)
		}
	}
	// A token that was valid is dead once the account is revoked (logout, password change): tokenCheck.
	st, _ := mwCall(t, "userRevokeSessions", obj{"user_id": u.ID})
	if st != 204 {
		t.Fatalf("revoke: %d", st)
	}
	time.Sleep(1100 * time.Millisecond) // the revocation takes effect at the next full second
	h.wantCode(401, "unauthorized", "GET", "/v1/services", good, nil)
}

func claimsOf(t *testing.T, token string) *praxisauth.UserClaims {
	t.Helper()
	pub := shared.jwtKey.Public().(ed25519.PublicKey)
	c, err := praxisauth.VerifyUser(token, praxisauth.StaticKeySet{praxisauth.KeyID(pub): pub}, praxisauth.AudienceAPI, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProtocolHygiene(t *testing.T) {
	h := newHarness(t)
	u := makeUser(t, "Olga", "organisator")
	h.wantCode(404, "not_found", "GET", "/v1/nothing", u.tok(), nil)
	h.want(200, "GET", "/healthz", "", nil)
	h.wantCode(422, "validation_failed", "POST", "/v1/specialties", u.tok(), obj{"name": "x", "extra": true})
	h.wantCode(422, "validation_failed", "POST", "/v1/specialties", u.tok(), obj{"name": ""})
	if st, _ := h.req("POST", "/v1/specialties", u.tok(), []byte(`{"name":`)); st != 400 && st != 422 {
		t.Errorf("broken JSON: %d", st)
	}
}
