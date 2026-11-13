// Package store implements the fixed database operations of api/database_middleware.yaml.
// Every statement is a constant with bound parameters. Soft-deleted rows are filtered here, centrally.
package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Error is a domain error that the HTTP layer turns into an application/problem+json answer.
type Error struct {
	Status int
	Code   string
	Title  string
}

func (e *Error) Error() string { return e.Code }

// ErrNotFound means no row matched (also: the owner filter did not match).
var ErrNotFound = &Error{Status: 404, Code: "not_found", Title: "Not found"}

func conflict(code, title string) *Error      { return &Error{Status: 409, Code: code, Title: title} }
func unprocessable(code, title string) *Error { return &Error{Status: 422, Code: code, Title: title} }

// Store holds the connection pool and the practice time zone used for working hours.
type Store struct {
	pool *pgxpool.Pool
	tz   string
}

// New creates a Store. tz is an IANA zone name, e.g. Europe/Berlin (decision 18).
func New(pool *pgxpool.Pool, tz string) *Store { return &Store{pool: pool, tz: tz} }

// Ping checks the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// inTx runs a block of database work as a single transaction
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// mapErr turns database errors into domain errors. Unknown errors stay as they are (HTTP 500, no details).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*Error](err); ok {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pe.Code {
		case "23505":
			return conflict("duplicate", "Already exists")
		case "23P01":
			return conflict("slot_taken", "Slot already taken")
		case "23514":
			return unprocessable("check_violation", "Value violates a rule")
		case "23503":
			return unprocessable("unknown_reference", "Referenced object does not exist or is in use")
		case "22P02", "22007", "22008", "22003", "22001":
			return unprocessable("invalid_value", "Invalid value")
		}
	}
	return err
}

// refAsNotFound maps a missing foreign key target to 404 instead of 422.
func refAsNotFound(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		return ErrNotFound
	}
	return mapErr(err)
}

// refAsConflict maps "still referenced" to 409 (delete operations).
// 23503 = NO ACTION at statement end, 23001 = RESTRICT (both mean: the row is still referenced).
func refAsConflict(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "23503" || pe.Code == "23001") {
		return conflict("in_use", "Object is still in use")
	}
	return mapErr(err)
}

func escapeLike(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

func limitOffset(limit, offset *int) (int, int) {
	l, o := 50, 0
	if limit != nil {
		l = *limit
	}
	if offset != nil {
		o = *offset
	}
	return l, o
}

func nilIfEmpty(p *string) *string {
	if p != nil && *p == "" {
		return nil
	}
	return p
}
