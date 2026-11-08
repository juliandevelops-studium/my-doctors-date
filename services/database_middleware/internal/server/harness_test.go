package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/server"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/store"
)

const hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"

type obj = map[string]any

type env struct {
	t    *testing.T
	url  string
	keys map[string]ed25519.PrivateKey
	pool *pgxpool.Pool
}

// newEnv starts the real HTTP stack against TEST_DATABASE_URL. The tests are skipped without it (make test-db).
func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test-db`")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	keys := map[string]ed25519.PrivateKey{}
	pubs := map[string]ed25519.PublicKey{}
	for _, name := range []string{"user_service", "calendar_service", "purge_job", "mail_service"} {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys[name], pubs[name] = priv, pub
	}
	verifier, err := praxisauth.NewServiceVerifier("database_middleware", pubs)
	if err != nil {
		t.Fatal(err)
	}
	h, err := server.New(server.NewHandler(store.New(pool, "Europe/Berlin")), verifier,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, url: srv.URL, keys: keys, pool: pool}
}

func (e *env) token(caller string) string {
	return praxisauth.MintServiceToken(e.keys[caller], caller, "database_middleware", "", time.Now(), uuid.NewString())
}

// raw sends a request and returns the status and the decoded JSON body (object or nil).
func (e *env) raw(token, op, contentType string, body []byte) (int, obj) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url+"/v1/ops/"+op, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out obj
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (e *env) call(caller, op string, body any) (int, obj) {
	e.t.Helper()
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			e.t.Fatal(err)
		}
	}
	return e.raw(e.token(caller), op, "application/json", b)
}

// ok calls and requires the given status.
func (e *env) ok(caller, op string, body any, want int) obj {
	e.t.Helper()
	got, out := e.call(caller, op, body)
	if got != want {
		e.t.Fatalf("%s as %s: status %d, want %d, body %v", op, caller, got, want, out)
	}
	return out
}

func (e *env) expect(caller, op string, body any, want int, code string) {
	e.t.Helper()
	got, out := e.call(caller, op, body)
	if got != want {
		e.t.Fatalf("%s as %s: status %d, want %d, body %v", op, caller, got, want, out)
	}
	if code != "" && out["code"] != code {
		e.t.Fatalf("%s: code %v, want %s", op, out["code"], code)
	}
}

func str(m obj, key string) string {
	s, _ := m[key].(string)
	return s
}

func list(m obj, key string) []any {
	l, _ := m[key].([]any)
	return l
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---- fixtures

func (e *env) newUser(status string, roles ...string) (id, email string) {
	e.t.Helper()
	email = fmt.Sprintf("u-%s@example.org", uuid.NewString())
	out := e.ok("user_service", "userCreate", obj{
		"first_name": "Test", "last_name": "User", "email": email, "password_hash": hash,
		"roles": roles, "status": status, "must_change_password": false,
	}, 201)
	return str(out, "id"), email
}

func (e *env) newDoctor() string {
	id, _ := e.newUser("active", "doctor")
	return id
}

func (e *env) newPatient() string {
	id, _ := e.newUser("active", "patient")
	return id
}

func (e *env) newSpecialty() string {
	return str(e.ok("calendar_service", "specialtyCreate", obj{"name": "Fach-" + uuid.NewString()}, 201), "id")
}

func (e *env) newService(specialty string, minutes int) string {
	return str(e.ok("calendar_service", "serviceCreate", obj{"name": "Leistung-" + uuid.NewString(), "duration_minutes": minutes, "specialty_id": specialty}, 201), "id")
}

// slot returns a start time in the future that is unique per call.
var slotCounter int

func slot() time.Time {
	slotCounter++
	return time.Now().Add(72 * time.Hour).Truncate(time.Hour).Add(time.Duration(slotCounter) * 24 * time.Hour)
}

func (e *env) book(caller, patient, doctor, service string, start time.Time, wantStatus int) obj {
	body := obj{"patient_id": patient, "service_id": service, "starts_at": ts(start), "ends_at": ts(start.Add(30 * time.Minute))}
	if doctor != "" {
		body["doctor_id"] = doctor
	}
	return e.ok(caller, "bookingCreate", body, wantStatus)
}
