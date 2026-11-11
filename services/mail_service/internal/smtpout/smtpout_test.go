package smtpout

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func sender(t *testing.T) *Sender {
	t.Helper()
	s, err := New(Config{Host: "localhost", Port: "25", TLS: TLSNone, FromAddress: "praxis@example.org", FromName: "Praxis Dr. Müller"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	return s
}

func TestMessageIsUTF8QuotedPrintable(t *testing.T) {
	id, raw, err := sender(t).Message("patient@example.org", "Änderung Ihrer E-Mail-Adresse", "Guten Tag Jörg,\n\nSchöne Grüße\n")
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subject != "Änderung Ihrer E-Mail-Adresse" {
		t.Errorf("subject: %q", subject)
	}
	if strings.ContainsAny(m.Header.Get("Subject"), "ÄÖÜäöüß") {
		t.Error("the subject header must be ASCII (RFC 2047)")
	}
	from, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("From"))
	if !strings.Contains(from, "Praxis Dr. Müller") || !strings.Contains(from, "<praxis@example.org>") {
		t.Errorf("from: %q", from)
	}
	if m.Header.Get("To") != "patient@example.org" || m.Header.Get("Message-Id") != "<"+id+">" || !strings.HasSuffix(id, "@example.org") {
		t.Errorf("headers: %v", m.Header)
	}
	if m.Header.Get("Auto-Submitted") != "auto-generated" || m.Header.Get("Content-Type") != "text/plain; charset=UTF-8" {
		t.Errorf("headers: %v", m.Header)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	if !strings.Contains(string(body), "Guten Tag Jörg,") || !strings.Contains(string(body), "Schöne Grüße") {
		t.Errorf("body: %q", body)
	}
}

func TestRecipientAndSubjectInjection(t *testing.T) {
	s := sender(t)
	for _, to := range []string{
		"a@example.org\r\nBcc: evil@example.org", "a@example.org\nBcc: evil@example.org", "Evil <a@example.org>", "a@example.org, b@example.org",
		"a@example.org;b@example.org", "\"a b\"@example.org", "", "no-at-sign", "a@", strings.Repeat("a", 250) + "@example.org", "a@example.org\x00",
	} {
		if _, _, err := s.Message(to, "Betreff", "Text"); err == nil {
			t.Errorf("recipient %q accepted", to)
		}
		if ValidAddress(to) {
			t.Errorf("ValidAddress(%q)", to)
		}
	}
	for _, subj := range []string{"A\r\nBcc: evil@example.org", "A\nB"} {
		if _, _, err := s.Message("a@example.org", subj, "Text"); err == nil {
			t.Errorf("subject %q accepted", subj)
		}
	}
	if !ValidAddress("erika.mustermann+praxis@example.org") {
		t.Error("a normal address must be valid")
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{Host: "h", Port: "25", TLS: TLSStartTLS, FromAddress: "a@example.org"}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"unknown TLS mode":        func(c *Config) { c.TLS = "maybe" },
		"credentials without TLS": func(c *Config) { c.TLS = TLSNone; c.Username = "u" },
		"bad sender":              func(c *Config) { c.FromAddress = "nope" },
	} {
		c := ok
		mod(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
