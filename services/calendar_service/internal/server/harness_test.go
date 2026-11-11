package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/server"
)

// The tests run the whole chain: calendar_service (in process) -> real database_middleware binary -> PostgreSQL.
// Users are created through middleware operations signed as user_service. They need TEST_DATABASE_URL (make test-db).

type obj = map[string]any

type sentMail struct {
	Template, To, FirstName, Service, Doctor string
}

type mailbox struct {
	mu   sync.Mutex
	sent []sentMail
}

func (m *mailbox) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

// last returns the newest mail of a template to an address.
func (m *mailbox) last(tpl, to string) (sentMail, bool) {
	all := m.all()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Template == tpl && strings.EqualFold(all[i].To, to) {
			return all[i], true
		}
	}
	return sentMail{}, false
}

var shared struct {
	once    sync.Once
	err     error
	mwURL   string
	userKey ed25519.PrivateKey // plays user_service
	calKey  ed25519.PrivateKey
	jwtKey  ed25519.PrivateKey // signs user tokens
	jwksURL string
	mailURL string
	mailbox *mailbox
	cmd     *exec.Cmd
	dir     string
	tz      *time.Location
}

func startShared(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test-db`")
	}
	shared.once.Do(func() { shared.err = bootShared(dsn) })
	if shared.err != nil {
		t.Fatal(shared.err)
	}
}

func bootShared(dsn string) error {
	var err error
	if shared.tz, err = time.LoadLocation("Europe/Berlin"); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "calendar-service-test")
	if err != nil {
		return err
	}
	bin := filepath.Join(dir, "database_middleware")
	build := exec.Command("go", "build", "-o", bin, "./cmd/database_middleware")
	build.Dir = "../../../database_middleware"
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build middleware: %v\n%s", err, out)
	}
	keys := filepath.Join(dir, "keys")
	if err := os.Mkdir(keys, 0o700); err != nil {
		return err
	}
	for name, dst := range map[string]*ed25519.PrivateKey{"user_service": &shared.userKey, "calendar_service": &shared.calKey} {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		*dst = priv
		der, _ := x509.MarshalPKIXPublicKey(pub)
		if err := os.WriteFile(filepath.Join(keys, name+".pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn, "SERVICE_KEYS_DIR="+keys, "LISTEN_ADDR="+addr)
	if err := cmd.Start(); err != nil {
		return err
	}
	shared.cmd, shared.dir, shared.mwURL = cmd, dir, "http://"+addr
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(shared.mwURL + "/healthz"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	_, shared.jwtKey, _ = ed25519.GenerateKey(rand.Reader)
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(praxisauth.NewJWKS(shared.jwtKey.Public().(ed25519.PublicKey)))
	}))
	shared.jwksURL = jwks.URL

	shared.mailbox = &mailbox{}
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Template string `json:"template"`
			To       string `json:"to"`
			Params   struct {
				FirstName  string `json:"first_name"`
				Service    string `json:"service"`
				DoctorName string `json:"doctor_name"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		shared.mailbox.mu.Lock()
		shared.mailbox.sent = append(shared.mailbox.sent, sentMail{req.Template, req.To, req.Params.FirstName, req.Params.Service, req.Params.DoctorName})
		shared.mailbox.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message_id":"` + uuid.NewString() + `"}`))
	}))
	shared.mailURL = mailSrv.URL
	return nil
}

type harness struct {
	t    *testing.T
	srv  *server.Server
	http *httptest.Server
	mail *mailbox
	now  time.Time
	mu   sync.Mutex
}

func newHarness(t *testing.T, bookingsPerMinute ...int) *harness {
	t.Helper()
	startShared(t)
	signer := praxisauth.NewServiceSigner("calendar_service", shared.calKey)
	database, err := db.New(shared.mwURL, signer)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := mailer.New(shared.mailURL, signer, log)
	if err != nil {
		t.Fatal(err)
	}
	keys := &praxisauth.RemoteKeySet{URL: shared.jwksURL}
	if err := keys.Load(); err != nil {
		t.Fatal(err)
	}
	auth := &praxisauth.Authenticator{Keys: keys, TTL: -1, Check: func(ctx context.Context, jti, sub string, iat time.Time) (bool, error) {
		var j, s db.Uuid
		if j.UnmarshalText([]byte(jti)) != nil || s.UnmarshalText([]byte(sub)) != nil {
			return false, nil
		}
		return database.TokenValid(ctx, j, s, iat)
	}}
	n := 100000
	if len(bookingsPerMinute) > 0 {
		n = bookingsPerMinute[0]
	}
	h := &harness{t: t, mail: shared.mailbox}
	h.srv = server.New(server.Deps{DB: database, Mail: m, Auth: auth, Log: log, BookingsPerMinute: n})
	handler, err := h.srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	h.http = httptest.NewServer(handler)
	t.Cleanup(h.http.Close)
	return h
}

// ---- requests

func (h *harness) req(method, path, token string, body any) (int, obj) {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out obj
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (h *harness) want(status int, method, path, token string, body any) obj {
	h.t.Helper()
	st, out := h.req(method, path, token, body)
	if st != status {
		h.t.Fatalf("%s %s: status %d, want %d, body %v", method, path, st, status, out)
	}
	return out
}

func (h *harness) wantCode(status int, code, method, path, token string, body any) obj {
	h.t.Helper()
	out := h.want(status, method, path, token, body)
	if code != "" && out["code"] != code {
		h.t.Fatalf("%s %s: code %v, want %s", method, path, out["code"], code)
	}
	return out
}

func str(m obj, k string) string { s, _ := m[k].(string); return s }
func items(m obj) []any          { l, _ := m["items"].([]any); return l }
func num(m obj, k string) int    { f, _ := m[k].(float64); return int(f) }

// ---- users (created through middleware operations, signed as user_service)

func mwCall(t *testing.T, op string, body any) (int, obj) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", shared.mwURL+"/v1/ops/"+op, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+praxisauth.MintServiceToken(shared.userKey, "user_service", "database_middleware", "", time.Now(), uuid.NewString()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out obj
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

type user struct {
	ID    string
	Email string
	First string
	Roles []string
	token string
}

func makeUser(t *testing.T, first string, roles ...string) *user {
	t.Helper()
	u := &user{Email: "t-" + uuid.NewString() + "@example.org", First: first, Roles: roles}
	st, out := mwCall(t, "userCreate", obj{"first_name": first, "last_name": "Test", "email": u.Email,
		"password_hash": "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA", "roles": roles, "status": "active", "must_change_password": false})
	if st != 201 {
		t.Fatalf("userCreate: %d %v", st, out)
	}
	u.ID = str(out, "id")
	return u
}

// tok signs a full-scope access token for the user (valid 15 minutes).
func (u *user) tok() string {
	if u.token == "" {
		now := time.Now()
		u.token = praxisauth.SignUser(shared.jwtKey, praxisauth.KeyID(shared.jwtKey.Public().(ed25519.PublicKey)), praxisauth.UserClaims{
			Issuer: praxisauth.UserIssuer, Audience: praxisauth.AudienceAPI, Subject: u.ID, Roles: u.Roles,
			IssuedAt: now.Unix(), Expires: now.Add(praxisauth.AccessTokenLifetime).Unix(), JTI: uuid.NewString(), MFA: true, Scope: praxisauth.ScopeFull})
	}
	return u.token
}

// ---- practice fixture

type practice struct {
	h         *harness
	org       *user // organisator
	front     *user // empfang
	specialty string
	service   string // 30 minutes, needs the specialty
	service60 string
	other     string // "Sonstiges"
	doctors   []*user
	monday    time.Time // 00:00 Berlin of a Monday, 3 weeks ahead
}

func nextWeekday(wd time.Weekday, weeksAhead int) time.Time {
	d := time.Now().In(shared.tz).AddDate(0, 0, 7*weeksAhead)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, shared.tz)
}

// at returns the instant of hh:mm Berlin time on the Monday (+ days) of the practice.
func (p *practice) at(days, hour, minute int) time.Time {
	d := p.monday.AddDate(0, 0, days)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, shared.tz).UTC()
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// instant parses a time of an answer; the offset depends on the machine of the middleware (UTC in the container).
func instant(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("time %q: %v", s, err)
	}
	return v
}

func sameInstant(t *testing.T, got string, want time.Time) bool {
	t.Helper()
	return instant(t, got).Equal(want)
}

// newPractice creates a specialty, services, `nDoctors` doctors working Monday to Friday 08:00-12:00, an organisator
// and a receptionist.
func newPractice(h *harness, nDoctors int) *practice {
	t := h.t
	p := &practice{h: h, monday: nextWeekday(time.Monday, 3)}
	p.org = makeUser(t, "Olga", "organisator")
	p.front = makeUser(t, "Emil", "desk")
	p.specialty = str(h.want(201, "POST", "/v1/specialties", p.org.tok(), obj{"name": "Fach-" + uuid.NewString()}), "id")
	p.service = str(h.want(201, "POST", "/v1/services", p.org.tok(), obj{"name": "Kontrolle-" + uuid.NewString(), "duration_minutes": 30, "specialty_id": p.specialty}), "id")
	p.service60 = str(h.want(201, "POST", "/v1/services", p.org.tok(), obj{"name": "Lang-" + uuid.NewString(), "duration_minutes": 60, "specialty_id": p.specialty}), "id")
	for _, sv := range items(h.want(200, "GET", "/v1/services", p.org.tok(), nil)) {
		if sv.(obj)["is_other"] == true {
			p.other = str(sv.(obj), "id")
		}
	}
	if p.other == "" {
		t.Fatal("service Sonstiges missing")
	}
	for i := 0; i < nDoctors; i++ {
		d := makeUser(t, fmt.Sprintf("Doc%d", i+1), "doctor")
		p.setSchedule(d, p.specialty, 8, 12)
		p.doctors = append(p.doctors, d)
	}
	return p
}

func (p *practice) setSchedule(d *user, specialty string, from, to int) {
	var days []obj
	for wd := 1; wd <= 5; wd++ {
		days = append(days, obj{"weekday": wd, "start_time": fmt.Sprintf("%02d:00", from), "end_time": fmt.Sprintf("%02d:00", to)})
	}
	p.h.want(200, "PUT", "/v1/doctors/"+d.ID+"/schedule", p.org.tok(), obj{"specialty_ids": []string{specialty}, "working_days": days})
}

func (p *practice) patient(name string) *user { return makeUser(p.h.t, name, "patient") }

// book is a patient booking; it returns status and body.
func (p *practice) book(u *user, service string, start time.Time, extra obj) (int, obj) {
	body := obj{"service_id": service, "starts_at": ts(start)}
	for k, v := range extra {
		body[k] = v
	}
	return p.h.req("POST", "/v1/bookings", u.tok(), body)
}

func (p *practice) mustBook(u *user, service string, start time.Time, extra obj) obj {
	p.h.t.Helper()
	st, out := p.book(u, service, start, extra)
	if st != 201 {
		p.h.t.Fatalf("booking %s: %d %v", start, st, out)
	}
	return out
}
