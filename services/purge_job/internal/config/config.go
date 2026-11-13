// Package config reads the settings of purge_job from the environment. The signing key comes from a mounted file.
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
)

// Config holds all settings.
type Config struct {
	MiddlewareURL string
	ServiceKey    ed25519.PrivateKey
	RunTimeout    time.Duration // upper bound for the whole run (DoS: no hanging job)
}

// Load reads and validates the configuration.
func Load() (*Config, error) {
	c := &Config{MiddlewareURL: os.Getenv("DATABASE_MIDDLEWARE_URL"), RunTimeout: 5 * time.Minute}
	if c.MiddlewareURL == "" {
		return nil, errors.New("DATABASE_MIDDLEWARE_URL is required")
	}
	if v := os.Getenv("RUN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > time.Hour {
			return nil, errors.New("RUN_TIMEOUT must be a duration between 1s and 1h")
		}
		c.RunTimeout = d
	}
	var err error
	if c.ServiceKey, err = praxisauth.LoadPrivateKey(os.Getenv("SERVICE_KEY_FILE")); err != nil {
		return nil, fmt.Errorf("SERVICE_KEY_FILE: %w", err)
	}
	return c, nil
}
