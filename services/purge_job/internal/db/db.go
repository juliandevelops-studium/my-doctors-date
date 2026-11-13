// Package db is the typed access of purge_job to database_middleware. Only the operations that purge_job may call
// exist in the generated client (x-allowed-callers): purgeExpired, bookingsCompletePast, retentionGet and auditWrite.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/mwclient"
)

// Category is a purge category of the middleware (docs/api/internal.md section 4).
type Category = mwclient.PurgeParamsCategory

// Purge categories.
const (
	BookingsCancelled        = mwclient.BookingsCancelled        // retention A
	BookingsCompletedContent = mwclient.BookingsCompletedContent // retention B
	BookingsCompleted        = mwclient.BookingsCompleted        // retention C
	Users                    = mwclient.Users                    // retention D
	AuditLog                 = mwclient.AuditLog                 // retention E
	Tokens                   = mwclient.Tokens
	EmailTokens              = mwclient.EmailTokens
)

// DB talks to database_middleware.
type DB struct{ c *mwclient.ClientWithResponses }

// New creates the client. Every request carries a fresh service token (iss=purge_job, at most 60 s).
func New(baseURL string, signer *praxisauth.ServiceSigner) (*DB, error) {
	c, err := mwclient.NewClientWithResponses(baseURL,
		mwclient.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		mwclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+signer.Token("database_middleware", ""))
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &DB{c: c}, nil
}

// failure turns a problem answer into an error that carries only status and code, never the body.
func failure(op string, status int, body []byte) error {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	return fmt.Errorf("%s: %s %s", op, http.StatusText(status), p.Code)
}

// PurgeExpired hard-deletes the expired rows of one category and returns how many rows were affected.
func (d *DB) PurgeExpired(ctx context.Context, cat Category) (int, error) {
	r, err := d.c.PurgeExpiredWithResponse(ctx, mwclient.PurgeParams{Category: cat})
	if err != nil {
		return 0, fmt.Errorf("purgeExpired(%s): %w", cat, err)
	}
	if r.JSON200 == nil {
		return 0, failure("purgeExpired("+string(cat)+")", r.StatusCode(), r.Body)
	}
	return r.JSON200.AffectedCount, nil
}

// BookingsCompletePast marks ended, confirmed bookings as completed and returns how many were changed.
func (d *DB) BookingsCompletePast(ctx context.Context) (int, error) {
	r, err := d.c.BookingsCompletePastWithResponse(ctx)
	if err != nil {
		return 0, fmt.Errorf("bookingsCompletePast: %w", err)
	}
	if r.JSON200 == nil {
		return 0, failure("bookingsCompletePast", r.StatusCode(), r.Body)
	}
	return r.JSON200.AffectedCount, nil
}
