// Package main is the entry point of the user_service: authentication, MFA, token issuing, own account,
// user and role management, audit log and retention settings.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/config"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/server"
)

func main() {
	time.Local = time.UTC // API times are always UTC (docs/api/CONVENTIONS.md), also on a developer machine
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	signer := praxisauth.NewServiceSigner("user_service", cfg.ServiceKey)
	database, err := db.New(cfg.MiddlewareURL, signer)
	if err != nil {
		return err
	}
	mail, err := mailer.New(cfg.MailURL, cfg.PublicBaseURL, signer, log)
	if err != nil {
		return err
	}
	srv := server.New(server.Deps{DB: database, Mail: mail, JWTKey: cfg.JWTKey, MFAKey: cfg.MFAKey, LoginWarnings: cfg.LoginWarnings, Log: log})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The server starts first so that the probes of the kubelet succeed while database_middleware is still coming up.
	if cfg.BootstrapEmail != "" {
		go bootstrap(ctx, log, srv, cfg.BootstrapEmail, cfg.BootstrapPassword)
	}

	handler, err := srv.Handler()
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("starting", "service", "user_service", "addr", cfg.ListenAddr)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// bootstrap creates the first system administrator (UF-03). The middleware may still be starting, so it retries until it
// worked or the service stops. Every failed attempt is logged.
func bootstrap(ctx context.Context, log *slog.Logger, srv *server.Server, email, pw string) {
	for {
		created, err := srv.EnsureBootstrapAdmin(ctx, email, pw)
		if err == nil {
			if created {
				log.Info("bootstrap administrator created; the password must be changed at the first login")
			}
			return
		}
		log.Warn("bootstrap administrator not created yet", "error", err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
