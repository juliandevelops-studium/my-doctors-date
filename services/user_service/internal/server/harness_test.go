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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mfa"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/server"
)

// The tests run the whole chain: user_service (in process) -> real database_middleware binary -> PostgreSQL.
// They need TEST_DATABASE_URL (make test-db) and skip otherwise.

type obj = map[string]any

type mail struct {
	Template string
	To       string
	Link     string
	Token    string
}

type mailbox struct {
	mu   sync.Mutex
	sent []mail
}

func (m *mailbox) all() []mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail(nil), m.sent...)
}

func (m *mailbox) count() int { return len(m.all()) }

// last returns the newest mail of a template to an address.
func (m *mailbox) last(tpl, to string) (mail, bool) {
	all := m.all()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Template == tpl && strings.EqualFold(all[i].To, to) {
			return all[i], true
		}
	}
	return mail{}, false
}

type harness struct {
	t      *testing.T
	srv    *server.Server
	http   *httptest.Server
	db     *db.DB
	mail   *mailbox
	jwtKey ed25519.PrivateKey
	mfaKey []byte
	offset time.Duration // fake clock offset
	mu     sync.Mutex
	pubKey ed25519.PublicKey
}

var shared struct {
	once       sync.Once
	err        error
	mwURL      string
	serviceKey ed25519.PrivateKey
	mailURL    string
	mailbox    *mailbox
	cmd        *exec.Cmd
	dir        string
}

func (h *harness) now() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return time.Now().Add(h.offset) }

// advance moves the clock of user_service forward (lock periods, TOTP time steps).
func (h *harness) advance(d time.Duration) { h.mu.Lock(); h.offset += d; h.mu.Unlock() }

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
	dir, err := os.MkdirTemp("", "user-service-test")
	if err != nil {
		return err
	}
	bin := filepath.Join(dir, "database_middleware")
	build := exec.Command("go", "build", "-o", bin, "./cmd/database_middleware")
	build.Dir = "../../../database_middleware"
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build middleware: %v\n%s", err, out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	shared.serviceKey = priv
	keys := filepath.Join(dir, "keys")
	if err := os.Mkdir(keys, 0o700); err != nil {
		return err
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	if err := os.WriteFile(filepath.Join(keys, "user_service.pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		return err
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
	shared.cmd, shared.dir = cmd, dir
	shared.mwURL = "http://" + addr
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(shared.mwURL + "/healthz"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	shared.mailbox = &mailbox{}
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Template string `json:"template"`
			To       string `json:"to"`
			Params   struct {
				Link string `json:"link"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		m := mail{Template: req.Template, To: req.To, Link: req.Params.Link}
		if i := strings.Index(m.Link, "token="); i >= 0 {
			m.Token = m.Link[i+len("token="):]
		}
		shared.mailbox.mu.Lock()
		shared.mailbox.sent = append(shared.mailbox.sent, m)
		shared.mailbox.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message_id":"` + uuid.NewString() + `"}`))
	}))
	shared.mailURL = mailSrv.URL
	return nil
}

type options struct {
	publicPerMinute int
	userPerMinute   int
	emailPerMinute  int
}

func newHarness(t *testing.T, opts ...options) *harness {
	t.Helper()
	startShared(t)
	o := options{publicPerMinute: 100000, userPerMinute: 100000, emailPerMinute: 100000}
	if len(opts) > 0 {
		o = opts[0]
	}
	_, jwtKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, mail: shared.mailbox, jwtKey: jwtKey, mfaKey: bytes.Repeat([]byte{9}, 32)}
	h.pubKey = jwtKey.Public().(ed25519.PublicKey)
	signer := praxisauth.NewServiceSigner("user_service", shared.serviceKey)
	h.db, err = db.New(shared.mwURL, signer)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := mailer.New(shared.mailURL, "https://praxis.test", signer, log)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = server.New(server.Deps{DB: h.db, Mail: m, JWTKey: jwtKey, MFAKey: h.mfaKey, Log: log, Now: h.now,
		PublicPerMinute: o.publicPerMinute, UserPerMinute: o.userPerMinute, EmailPerMinute: o.emailPerMinute, TokenCacheTTL: -1})
	handler, err := h.srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	h.http = httptest.NewServer(handler)
	t.Cleanup(h.http.Close)
	return h
}

// ---- requests

func (h *harness) req(method, path, token string, body any, headers ...string) (int, obj, http.Header) {
	h.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/json"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd, ct = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out obj
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

func (h *harness) do(method, path, token string, body any) (int, obj) {
	h.t.Helper()
	st, out, _ := h.req(method, path, token, body)
	return st, out
}

func (h *harness) want(status int, method, path, token string, body any) obj {
	h.t.Helper()
	st, out := h.do(method, path, token, body)
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

// ---- fixtures

const goodPassword = "Orange-Tisch-Lampe-Regen-42"

func uniqueEmail() string { return "t-" + uuid.NewString() + "@example.org" }

type account struct {
	ID       db.Uuid
	Email    string
	Password string
	Secret   []byte // TOTP secret if MFA is active
	lastStep int64
}

// makeUser creates an active account directly in the database. withMFA activates a confirmed TOTP secret.
func (h *harness) makeUser(withMFA bool, roles ...mwclient.Role) *account {
	h.t.Helper()
	hash, err := password.Hash(goodPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	a := &account{Email: uniqueEmail(), Password: goodPassword}
	u, err := h.db.UserCreate(context.Background(), mwclient.UserCreate{
		FirstName: "Test", LastName: "Person", Email: mwclient.Email(a.Email), PasswordHash: hash,
		Roles: roles, Status: mwclient.Active, MustChangePassword: false,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	a.ID = u.Id
	if withMFA {
		a.Secret = mfa.NewSecret()
		sealed, err := mfa.Encrypt(h.mfaKey, a.ID.String(), a.Secret)
		if err != nil {
			h.t.Fatal(err)
		}
		if err := h.db.MfaSecretUpsert(context.Background(), a.ID, sealed); err != nil {
			h.t.Fatal(err)
		}
		if err := h.db.MfaSecretConfirm(context.Background(), a.ID); err != nil {
			h.t.Fatal(err)
		}
	}
	return a
}

// code returns a valid TOTP code that has not been used yet. Steps -1, 0 and +1 are valid at the same time, so up to
// three codes are available without moving the clock; after that the clock is advanced as needed.
func (h *harness) code(a *account) string {
	h.t.Helper()
	cur := mfa.Step(h.now())
	s := a.lastStep + 1
	if s < cur-1 {
		s = cur - 1
	}
	if s > cur+1 {
		h.advance(time.Duration(s-(cur+1)) * mfa.Period * time.Second)
	}
	a.lastStep = s
	return mfa.Code(a.Secret, s)
}

// jumpTo sets the clock of user_service to an absolute time (lock periods are stamped by the database).
func (h *harness) jumpTo(t time.Time) { h.mu.Lock(); h.offset = time.Until(t); h.mu.Unlock() }

// lockedUntil reads the end of the current lock period from the database.
func (h *harness) lockedUntil(a *account) time.Time {
	h.t.Helper()
	u, err := h.db.UserGetByID(context.Background(), a.ID)
	if err != nil || u.LockedUntil == nil {
		h.t.Fatalf("no lock period: %v", err)
	}
	return *u.LockedUntil
}

// login runs both steps and returns the answer of the MFA step.
func (h *harness) login(a *account, code string) (int, obj) {
	h.t.Helper()
	st, ch := h.do("POST", "/v1/auth/login", "", obj{"email": a.Email, "password": a.Password})
	if st != 200 {
		return st, ch
	}
	body := obj{"mfa_challenge": ch["mfa_challenge"]}
	if code != "" {
		body["code"] = code
	}
	return h.do("POST", "/v1/auth/login/mfa", "", body)
}

// token logs in with MFA and returns a full-scope access token.
func (h *harness) token(a *account) string {
	h.t.Helper()
	st, out := h.login(a, h.code(a))
	if st != 200 || out["scope"] != "full" {
		h.t.Fatalf("login failed: %d %v", st, out)
	}
	return str(out, "access_token")
}

func claimsOf(t *testing.T, h *harness, token string) *praxisauth.UserClaims {
	t.Helper()
	c, err := praxisauth.VerifyUser(token, praxisauth.StaticKeySet{praxisauth.KeyID(h.pubKey): h.pubKey}, praxisauth.AudienceAPI, h.now())
	if err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	return c
}

// lockedUntil2 is the end of the lock period plus a margin, for jumpTo; it returns "now" if the account is not locked.
func (h *harness) lockedUntil2(a *account) time.Time {
	h.t.Helper()
	u, err := h.db.UserGetByID(context.Background(), a.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	if u.LockedUntil == nil {
		return time.Now()
	}
	return u.LockedUntil.Add(2 * time.Second)
}

// queryRow reads directly from the test database (only for checks that the API does not expose).
func queryRow(t *testing.T, sql string, args ...any) pgx.Row {
	t.Helper()
	if testPool == nil {
		pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		testPool = pool
	}
	return testPool.QueryRow(context.Background(), sql, args...)
}

var testPool *pgxpool.Pool

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func mwRole(r string) mwclient.Role { return mwclient.Role(r) }
