package job

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/db"
)

type fake struct {
	calls   []string
	failOn  string
	cancel  context.CancelFunc
	cancelA string
}

func (f *fake) BookingsCompletePast(context.Context) (int, error) {
	f.calls = append(f.calls, "complete")
	if f.failOn == "complete" {
		return 0, errors.New("boom")
	}
	return 1, nil
}

func (f *fake) PurgeExpired(_ context.Context, c db.Category) (int, error) {
	f.calls = append(f.calls, string(c))
	if f.cancel != nil && string(c) == f.cancelA {
		f.cancel()
	}
	if f.failOn == string(c) {
		return 0, errors.New("boom")
	}
	return 2, nil
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRunOrder(t *testing.T) {
	f := &fake{}
	if err := Run(context.Background(), f, quiet); err != nil {
		t.Fatal(err)
	}
	want := []string{"complete", "bookings_completed_content", "bookings_cancelled", "bookings_completed", "users", "tokens", "email_tokens", "audit_log"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
	}
}

func TestRunContinuesAfterFailure(t *testing.T) {
	f := &fake{failOn: "users"}
	err := Run(context.Background(), f, quiet)
	if err == nil {
		t.Fatal("want an error")
	}
	if f.calls[len(f.calls)-1] != "audit_log" {
		t.Fatalf("later categories must still run: %v", f.calls)
	}
}

func TestRunAbortsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{cancel: cancel, cancelA: "bookings_cancelled"}
	if err := Run(ctx, f, quiet); err == nil {
		t.Fatal("want an error")
	}
	if f.calls[len(f.calls)-1] != "bookings_cancelled" {
		t.Fatalf("run must stop after cancellation: %v", f.calls)
	}
}
