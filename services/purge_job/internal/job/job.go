// Package job is the run of the purge job: complete past bookings, then delete expired data category by category
// (retention concept A to E, docs/data-model.md). The middleware reads the retention periods itself and writes one audit
// row per purge call, so the job holds no deletion rules and logs only counters, never data.
package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/db"
)

// Backend is what the job needs from database_middleware.
type Backend interface {
	BookingsCompletePast(ctx context.Context) (int, error)
	PurgeExpired(ctx context.Context, cat db.Category) (int, error)
}

// Order of the purge calls. Bookings are completed first so that B and C see current states; users (D) go after their
// bookings; the audit log (E) goes last so that the audit rows of this run are never deleted by the same run.
var Order = []db.Category{
	db.BookingsCompletedContent, // B
	db.BookingsCancelled,        // A
	db.BookingsCompleted,        // C
	db.Users,                    // D
	db.Tokens,
	db.EmailTokens,
	db.AuditLog, // E
}

// Run executes all steps. A failing step does not stop the others (one category must not block the rest); all errors
// are returned joined so the process exits non-zero and Kubernetes records the failed run.
func Run(ctx context.Context, b Backend, log *slog.Logger) error {
	var errs []error
	if n, err := b.BookingsCompletePast(ctx); err != nil {
		log.Error("step failed", "step", "bookings_complete_past", "error", err.Error())
		errs = append(errs, err)
	} else {
		log.Info("step done", "step", "bookings_complete_past", "affected", n)
	}
	for _, cat := range Order {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("run aborted before %s: %w", cat, err))
			break
		}
		n, err := b.PurgeExpired(ctx, cat)
		if err != nil {
			log.Error("step failed", "step", "purge", "category", string(cat), "error", err.Error())
			errs = append(errs, err)
			continue
		}
		log.Info("step done", "step", "purge", "category", string(cat), "affected", n)
	}
	return errors.Join(errs...)
}
