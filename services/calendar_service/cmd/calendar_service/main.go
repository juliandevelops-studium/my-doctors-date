// Package main is the entry point of the calendar_service: services, doctors' schedules, the free/busy calendar,
// availability and bookings.
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
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/config"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/server"
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
	signer := praxisauth.NewServiceSigner("calendar_service", cfg.ServiceKey)
	database, err := db.New(cfg.MiddlewareURL, signer)
	if err != nil {
		return err
	}
	mail, err := mailer.New(cfg.MailURL, signer, log)
	if err != nil {
		return err
	}

	// The public keys of user_service. It may still be starting; unknown key ids trigger a refresh later anyway.
	keys := &praxisauth.RemoteKeySet{URL: cfg.JWKSURL}
	for i := 0; i < 10; i++ {
		if err := keys.Load(); err == nil {
			break
		} else if i == 9 {
			log.Warn("JWKS not loaded yet, will retry on the first token", "error", err.Error())
		}
		time.Sleep(time.Second)
	}
	auth := &praxisauth.Authenticator{Keys: keys, Check: func(ctx context.Context, jti, sub string, iat time.Time) (bool, error) {
		var j, s db.Uuid
		if j.UnmarshalText([]byte(jti)) != nil || s.UnmarshalText([]byte(sub)) != nil {
			return false, nil
		}
		return database.TokenValid(ctx, j, s, iat)
	}}

	srv := server.New(server.Deps{DB: database, Mail: mail, Auth: auth, Log: log})
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("starting", "service", "calendar_service", "addr", cfg.ListenAddr)
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
