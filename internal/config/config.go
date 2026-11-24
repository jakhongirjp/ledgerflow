// Package config loads twelve-factor style configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is shared by the ledgerd and relay binaries.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	Store           string // "postgres" or "memory"
	NATSURL         string
	SubjectPrefix   string
	RelayInterval   time.Duration
	RelayBatchSize  int
	ShutdownTimeout time.Duration
	LogLevel        string
}

// Load reads configuration and validates required values.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:        env("LEDGER_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		Store:           env("LEDGER_STORE", "postgres"),
		NATSURL:         os.Getenv("NATS_URL"),
		SubjectPrefix:   env("LEDGER_SUBJECT_PREFIX", "ledger.events"),
		LogLevel:        env("LOG_LEVEL", "info"),
		RelayBatchSize:  100,
		RelayInterval:   time.Second,
		ShutdownTimeout: 15 * time.Second,
	}
	var err error
	if v := os.Getenv("RELAY_INTERVAL"); v != "" {
		if c.RelayInterval, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("RELAY_INTERVAL: %w", err)
		}
	}
	if v := os.Getenv("RELAY_BATCH_SIZE"); v != "" {
		if c.RelayBatchSize, err = strconv.Atoi(v); err != nil || c.RelayBatchSize <= 0 {
			return c, fmt.Errorf("RELAY_BATCH_SIZE must be a positive integer")
		}
	}
	switch c.Store {
	case "memory":
	case "postgres":
		if c.DatabaseURL == "" {
			return c, fmt.Errorf("DATABASE_URL is required when LEDGER_STORE=postgres")
		}
	default:
		return c, fmt.Errorf("LEDGER_STORE must be postgres or memory, got %q", c.Store)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
