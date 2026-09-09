package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds runtime configuration, read from environment variables.
// The HTTP port stays on the existing --port flag in cmd/server/main.go.
type Config struct {
	DatabaseURL          string // AI_FACTORY_DATABASE_URL (default: local dev compose)
	JWTSecret            string // AI_FACTORY_JWT_SECRET (default "dev-secret-change-me")
	LogLevel             string // AI_FACTORY_LOG_LEVEL (default "info")
	KafkaAddr            string // AI_FACTORY_KAFKA_ADDR (default "localhost:9092")
	RedisAddr            string // AI_FACTORY_REDIS_ADDR (default "localhost:6379")
	RateLimitRPM         int    // AI_FACTORY_RATE_LIMIT_RPM (default 60)
	RateLimitConcurrency int    // AI_FACTORY_RATE_LIMIT_CONCURRENCY (default 4)
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	secret, ok := os.LookupEnv("AI_FACTORY_JWT_SECRET")
	if ok && secret == "" {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET is set but empty")
	}
	if secret == "" {
		secret = "dev-secret-change-me"
	}
	if len(secret) < 16 {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET must be at least 16 characters")
	}
	return &Config{
		DatabaseURL:          env("AI_FACTORY_DATABASE_URL", "postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable"),
		JWTSecret:            secret,
		LogLevel:             env("AI_FACTORY_LOG_LEVEL", "info"),
		KafkaAddr:            env("AI_FACTORY_KAFKA_ADDR", "localhost:9092"),
		RedisAddr:            env("AI_FACTORY_REDIS_ADDR", "localhost:6379"),
		RateLimitRPM:         envInt("AI_FACTORY_RATE_LIMIT_RPM", 60),
		RateLimitConcurrency: envInt("AI_FACTORY_RATE_LIMIT_CONCURRENCY", 4),
	}, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
