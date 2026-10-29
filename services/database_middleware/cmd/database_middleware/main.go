// Package main is the entry point of the database_middleware: the only component with database access (SR.02).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/server"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/store"
)

func main() {
	time.Local = time.UTC
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(log *slog.Logger) error {
	dbURL := os.Getenv("DATABASE_URL")
	keysDir := os.Getenv("SERVICE_KEYS_DIR")
	if dbURL == "" || keysDir == "" {
		return errors.New("DATABASE_URL and SERVICE_KEYS_DIR are required")
	}
	tz := env("PRACTICE_TZ", "Europe/Berlin")
	if _, err := time.LoadLocation(tz); err != nil {
		return err
	}
	keys, err := praxisauth.LoadPublicKeys(keysDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL") // never echo the URL
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool, tz)
	if err := waitForDatabase(ctx, log, st); err != nil {
		return err
	}

	verifier, err := praxisauth.NewServiceVerifier(env("SERVICE_AUDIENCE", "database_middleware"), keys)
	if err != nil {
		return err
	}
	handler, err := server.New(server.NewHandler(st), verifier, log)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("starting", "service", "database_middleware", "addr", srv.Addr, "callers", len(keys))
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// waitForDatabase retries the first connection for about a minute. In a cluster a new pod has no network rules for a
// moment and the database may be restarting; crashing on that would only make Kubernetes restart the pod.
func waitForDatabase(ctx context.Context, log *slog.Logger, st *store.Store) error {
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		if err = st.Ping(ctx); err == nil {
			return nil
		}
		log.Warn("database not reachable yet", "attempt", attempt, "error", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}
