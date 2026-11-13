package db

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
)

func newTestDB(t *testing.T, h http.HandlerFunc) *DB {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(srv.URL, praxisauth.NewServiceSigner("purge_job", priv))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPurgeExpired(t *testing.T) {
	d := newTestDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ops/purgeExpired" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("unexpected request %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"affected_count":7}`))
	})
	n, err := d.PurgeExpired(context.Background(), Users)
	if err != nil || n != 7 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestFailureCarriesOnlyStatusAndCode(t *testing.T) {
	d := newTestDB(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"forbidden","detail":"secret detail"}`))
	})
	_, err := d.BookingsCompletePast(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret detail") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err=%v", err)
	}
}
