// Package config reads the settings of user_service from the environment. Secrets come from mounted files
// (Kubernetes Secrets), never from the image or the repository.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
)

// Config holds all settings.
type Config struct {
	ListenAddr    string
	MiddlewareURL string
	MailURL       string
	// PublicBaseURL is the public address of the frontend; links in e-mails point there.
	PublicBaseURL string

	JWTKey     ed25519.PrivateKey // signs user tokens
	ServiceKey ed25519.PrivateKey // signs service tokens (iss=user_service)
	MFAKey     []byte             // 32 bytes: AES-256-GCM key for TOTP secrets and pepper for recovery codes

	BootstrapEmail    string
	BootstrapPassword string

	LoginWarnings bool
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func readFile(envName string) ([]byte, error) {
	path := os.Getenv(envName)
	if path == "" {
		return nil, fmt.Errorf("%s is required", envName)
	}
	b, err := os.ReadFile(path) //nolint:gosec // secret mount chosen by the operator
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envName, err)
	}
	return b, nil
}

// Load reads and validates the configuration.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		MiddlewareURL:  os.Getenv("DATABASE_MIDDLEWARE_URL"),
		MailURL:        os.Getenv("MAIL_SERVICE_URL"),
		PublicBaseURL:  strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		BootstrapEmail: os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		LoginWarnings:  os.Getenv("LOGIN_WARNINGS") == "true",
	}
	if c.MiddlewareURL == "" || c.MailURL == "" || c.PublicBaseURL == "" {
		return nil, errors.New("DATABASE_MIDDLEWARE_URL, MAIL_SERVICE_URL and PUBLIC_BASE_URL are required")
	}
	var err error
	if c.JWTKey, err = praxisauth.LoadPrivateKey(os.Getenv("JWT_KEY_FILE")); err != nil {
		return nil, fmt.Errorf("JWT_KEY_FILE: %w", err)
	}
	if c.ServiceKey, err = praxisauth.LoadPrivateKey(os.Getenv("SERVICE_KEY_FILE")); err != nil {
		return nil, fmt.Errorf("SERVICE_KEY_FILE: %w", err)
	}
	raw, err := readFile("MFA_KEY_FILE")
	if err != nil {
		return nil, err
	}
	if c.MFAKey, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(c.MFAKey) != 32 {
		return nil, errors.New("MFA_KEY_FILE must contain 32 random bytes, Base64 encoded")
	}
	if os.Getenv("BOOTSTRAP_ADMIN_PASSWORD_FILE") != "" {
		b, err := readFile("BOOTSTRAP_ADMIN_PASSWORD_FILE")
		if err != nil {
			return nil, err
		}
		c.BootstrapPassword = strings.TrimSpace(string(b))
	}
	if (c.BootstrapEmail == "") != (c.BootstrapPassword == "") {
		return nil, errors.New("BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD_FILE must be set together")
	}
	return c, nil
}
