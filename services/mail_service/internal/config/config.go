// Package config reads the settings of mail_service from the environment. Secrets come from mounted files.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds all settings.
type Config struct {
	ListenAddr     string
	ServiceKeysDir string
	LinkBase       string
	Practice       string
	Timezone       string

	SMTPHost, SMTPPort, SMTPTLS, SMTPFrom, SMTPFromName string
	SMTPUsername, SMTPPassword                          string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads and validates the configuration.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		ServiceKeysDir: os.Getenv("SERVICE_KEYS_DIR"),
		LinkBase:       strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		Practice:       env("PRACTICE_NAME", "Arztpraxis"),
		Timezone:       env("PRACTICE_TZ", "Europe/Berlin"),
		SMTPHost:       os.Getenv("SMTP_HOST"),
		SMTPPort:       env("SMTP_PORT", "587"),
		SMTPTLS:        env("SMTP_TLS", "starttls"),
		SMTPFrom:       os.Getenv("SMTP_FROM"),
		SMTPUsername:   os.Getenv("SMTP_USERNAME"),
	}
	c.SMTPFromName = env("SMTP_FROM_NAME", c.Practice)
	if c.ServiceKeysDir == "" || c.LinkBase == "" || c.SMTPHost == "" || c.SMTPFrom == "" {
		return nil, errors.New("SERVICE_KEYS_DIR, PUBLIC_BASE_URL, SMTP_HOST and SMTP_FROM are required")
	}
	if f := os.Getenv("SMTP_PASSWORD_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // secret mount chosen by the operator
		if err != nil {
			return nil, fmt.Errorf("SMTP_PASSWORD_FILE: %w", err)
		}
		c.SMTPPassword = strings.TrimSpace(string(b))
	}
	return c, nil
}
