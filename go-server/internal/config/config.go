package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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
	// InferenceMaxInFlightBatches is the Batch Slot count of the BatchScheduler.
	// With the worker's continuous batching, >1 lets requests queue up while
	// others decode (the worker still runs one forward pass at a time).
	InferenceMaxInFlightBatches int
	// InferenceMode picks the data-plane backend: "worker" (default) routes to
	// the gRPC Python worker (BatchScheduler); "openai" calls InferenceURL's
	// OpenAI-compatible API directly (vLLM, llama-server, …), bypassing the
	// worker. InferenceModel is the model name sent to that upstream.
	InferenceMode  string
	InferenceURL   string
	InferenceModel string
	Services       ServicesConfig
	Billing        BillingConfig
	Quota          QuotaConfig
	Tools          ToolsConfig

	// Resource budgets (C4) — declared in one place so the process cannot
	// silently run on database/sql defaults.
	DBMaxOpenConns        int
	DBMaxIdleConns        int
	DBConnMaxLifetime     time.Duration
	DBConnMaxIdleTime     time.Duration
	HTTPReadHeaderTimeout time.Duration
	HTTPIdleTimeout       time.Duration
}

// ServicesConfig selects which roles a process runs. Both default to true, so
// cmd/server keeps running API + in-process deployment worker.
type ServicesConfig struct {
	API     bool
	Worker  bool
	Billing bool
}

// BillingConfig configures prepaid billing. Mode is off|shadow|enforce;
// money amounts are integer micro-credits (1 credit = 1,000,000 µcr).
type BillingConfig struct {
	Mode             string
	Currency         string
	InitialAllowance int64
	ReservationTTL   time.Duration
	ReaperInterval   time.Duration
}

// QuotaConfig configures tenant quota enforcement against recorded usage.
// Mode is off|shadow|enforce (shadow logs + meters without blocking).
type QuotaConfig struct {
	Mode string
}

// ToolsConfig selects how the built-in tools execute. Executor is local (run on
// the host) or docker (run in a throwaway sandbox container, image configurable).
type ToolsConfig struct {
	Executor    string
	DockerImage string
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
	v.SetDefault("inference.max_in_flight_batches", 4)
	v.SetDefault("inference.mode", "worker")
	v.SetDefault("inference.url", "")
	v.SetDefault("inference.model", "")
	v.SetDefault("services.api", true)
	v.SetDefault("services.worker", true)
	v.SetDefault("services.billing", true)
	v.SetDefault("billing.mode", "shadow")
	v.SetDefault("billing.currency", "USD")
	v.SetDefault("billing.initial_allowance", 0)
	v.SetDefault("billing.reservation_ttl", "15m")
	v.SetDefault("billing.reaper_interval", "1m")
	v.SetDefault("quota.mode", "shadow")
	v.SetDefault("tools.executor", "local")
	v.SetDefault("tools.docker_image", "alpine:3.24")
	v.SetDefault("database_max_open_conns", 25)
	v.SetDefault("database_max_idle_conns", 25)
	v.SetDefault("database_conn_max_lifetime", "30m")
	v.SetDefault("database_conn_max_idle_time", "5m")
	v.SetDefault("http_read_header_timeout", "10s")
	v.SetDefault("http_idle_timeout", "120s")

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

	if mode := v.GetString("tools.executor"); mode != "local" && mode != "docker" {
		return nil, fmt.Errorf("tools.executor must be local or docker, got %q", mode)
	}

	if mode := v.GetString("quota.mode"); mode != "off" && mode != "shadow" && mode != "enforce" {
		return nil, fmt.Errorf("quota.mode must be off, shadow or enforce, got %q", mode)
	}

	if mode := v.GetString("inference.mode"); mode != "worker" && mode != "openai" {
		return nil, fmt.Errorf("inference.mode must be worker or openai, got %q", mode)
	}
	if v.GetString("inference.mode") == "openai" && v.GetString("inference.url") == "" {
		return nil, fmt.Errorf("inference.mode=openai requires inference.url")
	}

	return &Config{
		DatabaseURL:                 v.GetString("database_url"),
		JWTSecret:                   secret,
		LogLevel:                    v.GetString("log_level"),
		KafkaAddr:                   v.GetString("kafka_addr"),
		RedisAddr:                   v.GetString("redis_addr"),
		RateLimitRPM:                v.GetInt("rate_limit_rpm"),
		RateLimitConcurrency:        v.GetInt("rate_limit_concurrency"),
		InferenceMaxInFlightBatches: v.GetInt("inference.max_in_flight_batches"),
		InferenceMode:               v.GetString("inference.mode"),
		InferenceURL:                v.GetString("inference.url"),
		InferenceModel:              v.GetString("inference.model"),
		DBMaxOpenConns:              v.GetInt("database_max_open_conns"),
		DBMaxIdleConns:              v.GetInt("database_max_idle_conns"),
		DBConnMaxLifetime:           v.GetDuration("database_conn_max_lifetime"),
		DBConnMaxIdleTime:           v.GetDuration("database_conn_max_idle_time"),
		HTTPReadHeaderTimeout:       v.GetDuration("http_read_header_timeout"),
		HTTPIdleTimeout:             v.GetDuration("http_idle_timeout"),
		Services: ServicesConfig{
			API:     v.GetBool("services.api"),
			Worker:  v.GetBool("services.worker"),
			Billing: v.GetBool("services.billing"),
		},
		Billing: BillingConfig{
			Mode:             v.GetString("billing.mode"),
			Currency:         v.GetString("billing.currency"),
			InitialAllowance: v.GetInt64("billing.initial_allowance"),
			ReservationTTL:   v.GetDuration("billing.reservation_ttl"),
			ReaperInterval:   v.GetDuration("billing.reaper_interval"),
		},
		Quota: QuotaConfig{
			Mode: v.GetString("quota.mode"),
		},
		Tools: ToolsConfig{
			Executor:    v.GetString("tools.executor"),
			DockerImage: v.GetString("tools.docker_image"),
		},
	}, nil
}
