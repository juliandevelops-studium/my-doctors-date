package server_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/server"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/smtpout"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/templates"
)

type obj = map[string]any

// fakeSMTP is a minimal SMTP server that records the delivered messages.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []delivered
}

type delivered struct {
	From, To string
	Raw      string
}

func startSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fake ESMTP")
	var cur delivered
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = tp.PrintfLine("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			cur = delivered{From: strings.Trim(line[len("MAIL FROM:"):], "<> ")}
			_ = tp.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			cur.To = strings.Trim(line[len("RCPT TO:"):], "<> ")
			_ = tp.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 go ahead")
			raw, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			cur.Raw = string(raw)
			f.mu.Lock()
			f.msgs = append(f.msgs, cur)
			f.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}

func (f *fakeSMTP) all() []delivered {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]delivered(nil), f.msgs...)
}

type env struct {
	t       *testing.T
	url     string
	smtp    *fakeSMTP
	keys    map[string]ed25519.PrivateKey
	cleanup func()
}

const linkBase = "https://praxis.test"

func newEnv(t *testing.T, perRecipient int, smtpDown bool) *env {
	t.Helper()
	f := startSMTP(t)
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	if smtpDown {
		port = "1" // nothing listens there
	}
	sender, err := smtpout.New(smtpout.Config{Host: host, Port: port, TLS: smtpout.TLSNone, FromAddress: "praxis@example.org", FromName: "Praxis", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := templates.New("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, smtp: f, keys: map[string]ed25519.PrivateKey{}}
	pubs := map[string]ed25519.PublicKey{}
	for _, name := range []string{"user_service", "calendar_service", "database_middleware", "frontend_provider"} {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		e.keys[name], pubs[name] = priv, pub
	}
	verifier, err := praxisauth.NewServiceVerifier("mail_service", pubs)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Deps{Verifier: verifier, Renderer: renderer, Mailer: sender,
		LinkBase: linkBase, Practice: "Praxis Dr. Muster", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PerRecipientPerMinute: perRecipient})
	h, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	e.url = ts.URL
	return e
}

func (e *env) token(caller string) string {
	return praxisauth.MintServiceToken(e.keys[caller], caller, "mail_service", "", time.Now(), praxisauth.NewJTI())
}

func (e *env) send(token string, body any) (int, obj) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.url+"/v1/mails", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
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

func (e *env) want(status int, code, caller string, body obj) obj {
	e.t.Helper()
	st, out := e.send(e.token(caller), body)
	if st != status || (code != "" && out["code"] != code) {
		e.t.Fatalf("status %d (want %d), body %v", st, status, out)
	}
	return out
}

func mailReq(template, to string, params obj) obj {
	return obj{"template": template, "to": to, "params": params}
}

var when = time.Date(2030, 1, 2, 8, 30, 0, 0, time.UTC).Format(time.RFC3339)

// Every template, sent by its own caller, arrives as a readable German mail.
func TestEveryTemplateIsDelivered(t *testing.T) {
	e := newEnv(t, 1000, false)
	cases := []struct {
		caller, template string
		params           obj
		want             string
	}{
		{"user_service", "verify_email", obj{"first_name": "Erika", "link": linkBase + "/verify-email?token=abc"}, linkBase + "/verify-email?token=abc"},
		{"user_service", "password_reset", obj{"first_name": "Erika", "link": linkBase + "/reset-password?token=abc"}, "Passwort"},
		{"user_service", "email_change_confirm", obj{"first_name": "Erika", "link": linkBase + "/confirm-email-change?token=abc"}, "neue Adresse"},
		{"user_service", "email_change_notice", obj{"first_name": "Erika"}, "Änderung der E-Mail-Adresse"},
		{"user_service", "login_warning", obj{"first_name": "Erika", "time": when}, "02.01.2030, 09:30 Uhr"},
		{"user_service", "account_deleted", obj{"first_name": "Erika"}, "gelöscht"},
		{"calendar_service", "booking_confirmed", obj{"first_name": "Erika", "service": "Kontrolle", "starts_at": when, "doctor_name": "Dr. Muster"}, "Dr. Muster"},
		{"calendar_service", "booking_changed", obj{"first_name": "Erika", "service": "Kontrolle", "starts_at": when}, "wird von der Praxis zugeordnet"},
		{"calendar_service", "booking_cancelled", obj{"first_name": "Erika", "service": "Kontrolle", "starts_at": when}, "storniert"},
		{"calendar_service", "booking_reminder", obj{"first_name": "Erika", "service": "Kontrolle", "starts_at": when}, "erinnern"},
	}
	for i, c := range cases {
		to := "p" + string(rune('a'+i)) + "@example.org"
		out := e.want(202, "", c.caller, mailReq(c.template, to, c.params))
		if len(str(out, "message_id")) != 36 {
			t.Errorf("%s: message_id %v", c.template, out["message_id"])
		}
		m := e.smtp.all()[len(e.smtp.all())-1]
		if m.To != to || m.From != "praxis@example.org" {
			t.Errorf("%s: envelope %+v", c.template, m)
		}
		msg, err := mail.ReadMessage(strings.NewReader(m.Raw))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
		subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
		if !strings.Contains(string(body), c.want) || !strings.Contains(string(body), "Guten Tag Erika,") || subject == "" {
			t.Errorf("%s: subject %q, body:\n%s", c.template, subject, body)
		}
		if strings.Contains(subject, "Erika") {
			t.Errorf("%s: personal data in the subject", c.template)
		}
	}
}

func str(m obj, k string) string { s, _ := m[k].(string); return s }

func TestAuthentication(t *testing.T) {
	e := newEnv(t, 1000, false)
	body := mailReq("account_deleted", "a@example.org", obj{"first_name": "A"})
	if st, _ := e.send("", body); st != 401 {
		t.Errorf("no token: %d", st)
	}
	if st, _ := e.send("abc.def.ghi", body); st != 401 {
		t.Errorf("garbage: %d", st)
	}
	wrongAud := praxisauth.MintServiceToken(e.keys["user_service"], "user_service", "database_middleware", "", time.Now(), "j")
	if st, _ := e.send(wrongAud, body); st != 401 {
		t.Errorf("token for another service: %d", st)
	}
	_, forgedKey, _ := ed25519.GenerateKey(rand.Reader)
	forged := praxisauth.MintServiceToken(forgedKey, "user_service", "mail_service", "", time.Now(), "j")
	if st, _ := e.send(forged, body); st != 401 {
		t.Errorf("forged signature: %d", st)
	}
	expired := praxisauth.MintServiceToken(e.keys["user_service"], "user_service", "mail_service", "", time.Now().Add(-5*time.Minute), "j")
	if st, _ := e.send(expired, body); st != 401 {
		t.Errorf("expired: %d", st)
	}
	// Known services that are not callers of this route: 403, not 401 (the identity is genuine).
	for _, c := range []string{"database_middleware", "frontend_provider"} {
		if st, out := e.send(e.token(c), body); st != 403 || out["code"] != "forbidden" {
			t.Errorf("%s: %d %v", c, st, out)
		}
	}
	if n := len(e.smtp.all()); n != 0 {
		t.Fatalf("%d mails were sent without authorisation", n)
	}
}

// Decision 14: each caller may send only its own templates.
func TestTemplateAllowListPerCaller(t *testing.T) {
	e := newEnv(t, 1000, false)
	e.want(403, "template_not_allowed", "calendar_service", mailReq("verify_email", "a@example.org", obj{"first_name": "A", "link": linkBase + "/x"}))
	e.want(403, "template_not_allowed", "calendar_service", mailReq("password_reset", "a@example.org", obj{"first_name": "A", "link": linkBase + "/x"}))
	e.want(403, "template_not_allowed", "user_service", mailReq("booking_confirmed", "a@example.org", obj{"first_name": "A", "service": "S", "starts_at": when}))
	if n := len(e.smtp.all()); n != 0 {
		t.Fatalf("%d mails were sent", n)
	}
}

func TestParametersAreStrict(t *testing.T) {
	e := newEnv(t, 1000, false)
	ok := obj{"first_name": "Erika", "link": linkBase + "/verify-email?token=abc"}
	with := func(mod func(obj)) obj {
		p := obj{}
		for k, v := range ok {
			p[k] = v
		}
		mod(p)
		return mailReq("verify_email", "a@example.org", p)
	}
	for name, body := range map[string]obj{
		"missing first_name": with(func(p obj) { delete(p, "first_name") }),
		"missing link":       with(func(p obj) { delete(p, "link") }),
		"empty first_name":   with(func(p obj) { p["first_name"] = "  " }),
		"extra parameter":    with(func(p obj) { p["service"] = "Kontrolle" }),
		"unknown parameter":  with(func(p obj) { p["body"] = "Hallo" }),
		"foreign link":       with(func(p obj) { p["link"] = "https://evil.example/phish" }),
		"look-alike link":    with(func(p obj) { p["link"] = "https://praxis.test.evil.example/x" }),
		"link without path":  with(func(p obj) { p["link"] = linkBase }),
		"line break in name": with(func(p obj) { p["first_name"] = "Erika\r\nBcc: evil@example.org" }),
		"control character":  with(func(p obj) { p["first_name"] = "Erika\x07" }),
		"too long name":      with(func(p obj) { p["first_name"] = strings.Repeat("a", 101) }),
		"no params":          obj{"template": "verify_email", "to": "a@example.org"},
		"unknown template":   obj{"template": "newsletter", "to": "a@example.org", "params": obj{}},
		"free text":          obj{"template": "verify_email", "to": "a@example.org", "params": ok, "body": "Hallo"},
	} {
		st, out := e.send(e.token("user_service"), body)
		if st != 422 {
			t.Errorf("%s: status %d, want 422 (%v)", name, st, out)
		}
	}
	// booking mails: the doctor is optional, the time is required.
	e.want(422, "", "calendar_service", mailReq("booking_cancelled", "a@example.org", obj{"first_name": "A", "service": "S"}))
	e.want(422, "", "calendar_service", mailReq("booking_cancelled", "a@example.org", obj{"first_name": "A", "service": "S", "starts_at": when, "doctor_name": "Dr. X"}))
	if n := len(e.smtp.all()); n != 0 {
		t.Fatalf("%d mails were sent for invalid requests", n)
	}
}

func TestRecipientInjection(t *testing.T) {
	e := newEnv(t, 1000, false)
	for _, to := range []string{"a@example.org\r\nBcc: evil@example.org", "a@example.org,b@example.org", "Evil <a@example.org>", "not-an-address", ""} {
		st, _ := e.send(e.token("user_service"), mailReq("account_deleted", to, obj{"first_name": "A"}))
		if st != 422 {
			t.Errorf("recipient %q: %d, want 422", to, st)
		}
	}
	if n := len(e.smtp.all()); n != 0 {
		t.Fatalf("%d mails were sent", n)
	}
}

func TestRecipientRateLimit(t *testing.T) {
	e := newEnv(t, 3, false)
	for i := 0; i < 3; i++ {
		e.want(202, "", "user_service", mailReq("account_deleted", "flood@example.org", obj{"first_name": "A"}))
	}
	e.want(429, "rate_limited", "user_service", mailReq("account_deleted", "FLOOD@example.org", obj{"first_name": "A"}))
	e.want(202, "", "user_service", mailReq("account_deleted", "other@example.org", obj{"first_name": "A"}))
	if n := len(e.smtp.all()); n != 4 {
		t.Fatalf("%d mails delivered, want 4", n)
	}
}

func TestSMTPFailureIsA502WithoutDetails(t *testing.T) {
	e := newEnv(t, 1000, true)
	st, out := e.send(e.token("user_service"), mailReq("account_deleted", "a@example.org", obj{"first_name": "A"}))
	if st != 502 || out["code"] != "smtp_failed" {
		t.Fatalf("%d %v", st, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "127.0.0.1") || strings.Contains(string(raw), "refused") {
		t.Fatalf("the answer leaks connection details: %s", raw)
	}
}

func TestProtocolHygiene(t *testing.T) {
	e := newEnv(t, 1000, false)
	post := func(ct, body string) int {
		req, _ := http.NewRequest("POST", e.url+"/v1/mails", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.token("user_service"))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if st := post("text/plain", "{}"); st != 415 {
		t.Errorf("content type: %d", st)
	}
	if st := post("application/json", `{"template":`); st != 400 && st != 422 {
		t.Errorf("broken JSON: %d", st)
	}
	big := `{"template":"account_deleted","to":"a@example.org","params":{"first_name":"` + strings.Repeat("a", 20<<10) + `"}}`
	if st := post("application/json", big); st != 413 && st != 422 {
		t.Errorf("oversized: %d", st)
	}
	resp, err := http.Get(e.url + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz: %v", err)
	}
}
