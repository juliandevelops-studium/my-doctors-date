// Package config reads the settings of calendar_service from the environment. Secrets come from mounted files.
package config

import (
	"crypto/ed25519"
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
	JWKSURL       string // public keys of user_service
	Timezone      string
	ServiceKey    ed25519.PrivateKey
}

// Load reads and validates the configuration.
func Load() (*Config, error) {
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	c := &Config{
		ListenAddr:    get("LISTEN_ADDR", ":8080"),
		MiddlewareURL: os.Getenv("DATABASE_MIDDLEWARE_URL"),
		MailURL:       os.Getenv("MAIL_SERVICE_URL"),
		JWKSURL:       os.Getenv("USER_SERVICE_JWKS_URL"),
		Timezone:      get("PRACTICE_TZ", "Europe/Berlin"),
	}
	if c.MiddlewareURL == "" || c.MailURL == "" || c.JWKSURL == "" {
		return nil, errors.New("DATABASE_MIDDLEWARE_URL, MAIL_SERVICE_URL and USER_SERVICE_JWKS_URL are required")
	}
	if !strings.HasPrefix(c.JWKSURL, "http") {
		return nil, fmt.Errorf("USER_SERVICE_JWKS_URL must be an http(s) URL")
	}
	var err error
	if c.ServiceKey, err = praxisauth.LoadPrivateKey(os.Getenv("SERVICE_KEY_FILE")); err != nil {
		return nil, fmt.Errorf("SERVICE_KEY_FILE: %w", err)
	}
	return c, nil
}
