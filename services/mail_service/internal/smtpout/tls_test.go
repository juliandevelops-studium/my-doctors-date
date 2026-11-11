package smtpout

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// testServer is a small SMTP server with optional implicit TLS, STARTTLS and AUTH PLAIN.
type testServer struct {
	ln       net.Listener
	cert     tls.Certificate
	pool     *x509.CertPool
	implicit bool
	startTLS bool
	user     string
	pass     string

	mu   sync.Mutex
	got  []string
	auth []string
}

func newCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startTestServer(t *testing.T, implicit, startTLS bool, user, pass string) *testServer {
	t.Helper()
	cert, pool := newCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{ln: ln, cert: cert, pool: pool, implicit: implicit, startTLS: startTLS, user: user, pass: pass}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go ts.serve(c)
		}
	}()
	return ts
}

func (ts *testServer) serve(raw net.Conn) {
	defer raw.Close()
	cfg := &tls.Config{Certificates: []tls.Certificate{ts.cert}, MinVersion: tls.VersionTLS12}
	var c net.Conn = raw
	if ts.implicit {
		c = tls.Server(raw, cfg)
	}
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 test ESMTP")
	secure := ts.implicit
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			_ = tp.PrintfLine("250-test")
			if ts.startTLS && !secure {
				_ = tp.PrintfLine("250-STARTTLS")
			}
			if ts.user != "" && secure {
				_ = tp.PrintfLine("250-AUTH PLAIN")
			}
			_ = tp.PrintfLine("250 OK")
		case up == "STARTTLS" && !ts.startTLS:
			_ = tp.PrintfLine("502 command not implemented")
		case up == "STARTTLS":
			_ = tp.PrintfLine("220 go ahead")
			c = tls.Server(raw, cfg)
			tp = textproto.NewConn(c)
			secure = true
		case strings.HasPrefix(up, "AUTH PLAIN "):
			b, _ := base64.StdEncoding.DecodeString(line[len("AUTH PLAIN "):])
			ts.mu.Lock()
			ts.auth = append(ts.auth, strings.ReplaceAll(string(b), "\x00", "|"))
			ts.mu.Unlock()
			if string(b) == "\x00"+ts.user+"\x00"+ts.pass {
				_ = tp.PrintfLine("235 ok")
			} else {
				_ = tp.PrintfLine("535 bad credentials")
			}
		case up == "DATA":
			_ = tp.PrintfLine("354 go")
			b, _ := io.ReadAll(tp.DotReader())
			ts.mu.Lock()
			ts.got = append(ts.got, string(b))
			ts.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case up == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}

func (ts *testServer) cfg(mode string) Config {
	_, port, _ := net.SplitHostPort(ts.ln.Addr().String())
	return Config{Host: "localhost", Port: port, TLS: mode, FromAddress: "praxis@example.org", Timeout: 3 * time.Second, RootCAs: ts.pool}
}

func send(t *testing.T, c Config) error {
	t.Helper()
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Send(context.Background(), "patient@example.org", "Betreff", "Text\n")
	return err
}

func TestImplicitTLS(t *testing.T) {
	ts := startTestServer(t, true, false, "", "")
	if err := send(t, ts.cfg(TLSImplicit)); err != nil || len(ts.got) != 1 {
		t.Fatalf("implicit TLS: %v (%d delivered)", err, len(ts.got))
	}
}

func TestStartTLSAndAuthentication(t *testing.T) {
	ts := startTestServer(t, false, true, "mailer", "s3cret-pass")
	c := ts.cfg(TLSStartTLS)
	c.Username, c.Password = "mailer", "s3cret-pass"
	if err := send(t, c); err != nil || len(ts.got) != 1 {
		t.Fatalf("STARTTLS with login: %v", err)
	}
	if len(ts.auth) != 1 || ts.auth[0] != "|mailer|s3cret-pass" {
		t.Fatalf("auth exchange: %v", ts.auth)
	}
	c.Password = "wrong"
	if err := send(t, c); err == nil {
		t.Fatal("wrong credentials accepted")
	}
}

func TestCertificateIsAlwaysVerified(t *testing.T) {
	ts := startTestServer(t, true, false, "", "")
	c := ts.cfg(TLSImplicit)
	c.RootCAs = nil // the self-signed certificate is not in the system roots
	if err := send(t, c); err == nil || len(ts.got) != 0 {
		t.Fatalf("an untrusted certificate must be refused (implicit): %v", err)
	}
	ts2 := startTestServer(t, false, true, "", "")
	c2 := ts2.cfg(TLSStartTLS)
	c2.RootCAs = nil
	if err := send(t, c2); err == nil || len(ts2.got) != 0 {
		t.Fatalf("an untrusted certificate must be refused (STARTTLS): %v", err)
	}
}

func TestNoDowngradeWhenStartTLSIsMissing(t *testing.T) {
	ts := startTestServer(t, false, false, "", "") // does not offer STARTTLS and rejects the command
	if err := send(t, ts.cfg(TLSStartTLS)); err == nil || len(ts.got) != 0 {
		t.Fatalf("a server without STARTTLS must not receive the mail in clear text: %v", err)
	}
}
