// Package config reads runtime configuration from the environment.
//
// Every value has a safe default for local development. Production sets them
// in the process environment by whatever deploys it; nothing is read from the repo.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Config is the process configuration.
type Config struct {
	Listen       string     // address to listen on, e.g. "127.0.0.1:8080"
	DatabasePath string     // path to the SQLite database file
	LogLevel     slog.Level // minimum level to emit
}

// FromEnv builds a Config from SIGNUP_* environment variables.
func FromEnv() (Config, error) {
	cfg := Config{
		Listen:       envOr("SIGNUP_LISTEN", "127.0.0.1:8080"),
		DatabasePath: envOr("SIGNUP_DB", "signup.db"),
		LogLevel:     slog.LevelInfo,
	}
	switch strings.ToLower(envOr("SIGNUP_LOG_LEVEL", "info")) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return cfg, fmt.Errorf("SIGNUP_LOG_LEVEL: unknown level %q", os.Getenv("SIGNUP_LOG_LEVEL"))
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
