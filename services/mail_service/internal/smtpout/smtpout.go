// Package smtpout builds the message and hands it to the SMTP server (Mailpit in the demo, a provider in production).
package smtpout

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TLS modes.
const (
	TLSNone     = "none"     // plain SMTP, only for a local test server such as Mailpit
	TLSStartTLS = "starttls" // upgrade on port 587
	TLSImplicit = "tls"      // TLS from the first byte, port 465
)

// Config describes the SMTP server and the sender.
type Config struct {
	Host, Port  string
	TLS         string
	Username    string
	Password    string
	FromAddress string
	FromName    string
	Timeout     time.Duration
	// RootCAs replaces the system roots when verifying the server certificate (private CA of a mail relay, tests).
	// The certificate is always verified.
	RootCAs *x509.CertPool
}

// Sender sends mails.
type Sender struct {
	cfg Config
	now func() time.Time
}

// New validates the configuration.
func New(cfg Config) (*Sender, error) {
	switch cfg.TLS {
	case TLSNone, TLSStartTLS, TLSImplicit:
	default:
		return nil, fmt.Errorf("SMTP_TLS must be %s, %s or %s", TLSNone, TLSStartTLS, TLSImplicit)
	}
	if cfg.Username != "" && cfg.TLS == TLSNone {
		return nil, errors.New("SMTP credentials must not be sent without TLS")
	}
	if _, err := mail.ParseAddress(cfg.FromAddress); err != nil {
		return nil, fmt.Errorf("SMTP_FROM: %w", err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Sender{cfg: cfg, now: time.Now}, nil
}

// ValidAddress accepts a plain address (no display name, no comments, no control characters).
func ValidAddress(s string) bool {
	if s == "" || len(s) > 254 || strings.ContainsAny(s, "\r\n\x00 <>\",;") {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// Message builds the RFC 5322 message: UTF-8 subject (RFC 2047), quoted-printable plain text body.
func (s *Sender) Message(to, subject, body string) (id string, msg []byte, err error) {
	if !ValidAddress(to) {
		return "", nil, errors.New("invalid recipient")
	}
	if strings.ContainsAny(subject, "\r\n") {
		return "", nil, errors.New("invalid subject")
	}
	from := (&mail.Address{Name: s.cfg.FromName, Address: s.cfg.FromAddress}).String()
	_, domain, _ := strings.Cut(s.cfg.FromAddress, "@")
	id = uuid.NewString() + "@" + domain
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", s.now().Format(time.RFC1123Z))
	h("Message-ID", "<"+id+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(body)); err != nil {
		return "", nil, err
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return id, b.Bytes(), nil
}

// Send builds and delivers one message and returns its Message-ID.
func (s *Sender) Send(ctx context.Context, to, subject, body string) (string, error) {
	id, msg, err := s.Message(to, subject, body)
	if err != nil {
		return "", err
	}
	if err := s.deliver(ctx, to, msg); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Sender) deliver(ctx context.Context, to string, msg []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)
	d := &net.Dialer{Timeout: s.cfg.Timeout}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.cfg.RootCAs}
	var conn net.Conn
	var err error
	if s.cfg.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(s.cfg.Timeout)); err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.cfg.TLS == TLSStartTLS {
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(s.cfg.FromAddress); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
