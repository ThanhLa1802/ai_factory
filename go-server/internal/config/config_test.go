package config

import (
	"os"
	"testing"
)

// unsetenv removes key for the duration of the test, restoring the prior value.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%q): %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "")
	unsetenv(t, "AI_FACTORY_JWT_SECRET")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "")
	t.Setenv("AI_FACTORY_KAFKA_ADDR", "")
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
	if cfg.KafkaAddr != "localhost:9092" {
		t.Errorf("KafkaAddr = %q, want localhost:9092", cfg.KafkaAddr)
	}
}

func TestLoadJWTSecretSetButEmpty(t *testing.T) {
	t.Setenv("AI_FACTORY_JWT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want set-but-empty to fail loudly")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("AI_FACTORY_JWT_SECRET", "0123456789abcdef")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "debug")
	t.Setenv("AI_FACTORY_KAFKA_ADDR", "localhost:19092")
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
	if cfg.KafkaAddr != "localhost:19092" {
		t.Errorf("KafkaAddr = %q, want localhost:19092", cfg.KafkaAddr)
	}
}
