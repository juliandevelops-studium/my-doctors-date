// Package main is the entry point of the purge_job: a one-shot process started daily by a Kubernetes CronJob. It calls
// the purge operations of database_middleware and exits. It listens on no port and logs only counters.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/config"
	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/purge_job/internal/job"
)

func main() {
	time.Local = time.UTC
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("purge run failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	database, err := db.New(cfg.MiddlewareURL, praxisauth.NewServiceSigner("purge_job", cfg.ServiceKey))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.RunTimeout)
	defer cancel()

	start := time.Now()
	log.Info("purge run started")
	if err := job.Run(ctx, database, log); err != nil {
		return err
	}
	log.Info("purge run finished", "duration_ms", time.Since(start).Milliseconds())
	return nil
}
