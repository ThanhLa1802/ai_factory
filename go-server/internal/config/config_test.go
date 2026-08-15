package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "")
	t.Setenv("AI_FACTORY_JWT_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.JWTSecret == "" {
		t.Error("JWTSecret empty, want non-empty default for dev")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("AI_FACTORY_JWT_SECRET", "0123456789abcdef")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://u:p@localhost:5432/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != "0123456789abcdef" {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}
