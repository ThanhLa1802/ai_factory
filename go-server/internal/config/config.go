package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds runtime configuration. Values come from configs/config.yaml,
// overridden by AI_FACTORY_* environment variables. Fields stay flat so this
// phase does not churn any consumer.
type Config struct {
	DatabaseURL          string
	JWTSecret            string
	LogLevel             string
	KafkaAddr            string
	RedisAddr            string
	RateLimitRPM         int
	RateLimitConcurrency int
}

// Load reads configuration from an optional YAML file plus AI_FACTORY_* env
// overrides. path == "" loads defaults + env only.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetDefault("database_url", "postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable")
	v.SetDefault("jwt_secret", "dev-secret-change-me")
	v.SetDefault("log_level", "info")
	v.SetDefault("kafka_addr", "localhost:9092")
	v.SetDefault("redis_addr", "localhost:6379")
	v.SetDefault("rate_limit_rpm", 60)
	v.SetDefault("rate_limit_concurrency", 4)

	v.SetEnvPrefix("AI_FACTORY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	// JWT secret keeps its strict, set-but-empty semantics (viper treats an
	// empty env var as unset, which is not what we want for a secret).
	secret, ok := os.LookupEnv("AI_FACTORY_JWT_SECRET")
	if ok && secret == "" {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET is set but empty")
	}
	if secret == "" {
		secret = v.GetString("jwt_secret")
	}
	if len(secret) < 16 {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET must be at least 16 characters")
	}

	return &Config{
		DatabaseURL:          v.GetString("database_url"),
		JWTSecret:            secret,
		LogLevel:             v.GetString("log_level"),
		KafkaAddr:            v.GetString("kafka_addr"),
		RedisAddr:            v.GetString("redis_addr"),
		RateLimitRPM:         v.GetInt("rate_limit_rpm"),
		RateLimitConcurrency: v.GetInt("rate_limit_concurrency"),
	}, nil
}
