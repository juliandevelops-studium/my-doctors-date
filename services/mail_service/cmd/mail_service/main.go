// Package main is the entry point of the mail_service: it sends the fixed set of templated mails over SMTP.
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
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/config"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/server"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/smtpout"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/templates"
)

func main() {
	time.Local = time.UTC // API times are always UTC (api/CONVENTIONS.md), also on a developer machine
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
	keys, err := praxisauth.LoadPublicKeys(cfg.ServiceKeysDir)
	if err != nil {
		return err
	}
	renderer, err := templates.New(cfg.Timezone)
	if err != nil {
		return err
	}
	sender, err := smtpout.New(smtpout.Config{Host: cfg.SMTPHost, Port: cfg.SMTPPort, TLS: cfg.SMTPTLS, Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword, FromAddress: cfg.SMTPFrom, FromName: cfg.SMTPFromName})
	if err != nil {
		return err
	}
	if cfg.SMTPTLS == smtpout.TLSNone {
		log.Warn("SMTP without TLS: only for a local test server such as Mailpit")
	}
	verifier, err := praxisauth.NewServiceVerifier("mail_service", keys)
	if err != nil {
		return err
	}
	srv := server.New(server.Deps{
		Verifier: verifier, Renderer: renderer, Mailer: sender,
		LinkBase: cfg.LinkBase, Practice: cfg.Practice, Log: log,
	})
	handler, err := srv.Handler()
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      40 * time.Second, // SMTP delivery may take a while
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("starting", "service", "mail_service", "addr", cfg.ListenAddr, "callers", len(keys))
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
