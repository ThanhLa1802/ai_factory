package app

import (
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"github.com/ai-factory/go-server/internal/infrastructure/database"
	infrainf "github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/ai-factory/go-server/internal/infrastructure/outbox"
	"github.com/ai-factory/go-server/internal/services/billing"
	"github.com/ai-factory/go-server/internal/services/iam"
	inferencesvc "github.com/ai-factory/go-server/internal/services/inference"
	"github.com/ai-factory/go-server/internal/services/serving"
	"github.com/ai-factory/go-server/internal/services/usage"
	"github.com/ai-factory/go-server/pkg/di"
	"github.com/redis/go-redis/v9"
)

// busBundle groups the event bus with whether it is the real Kafka bus.
type busBundle struct {
	Producer message.Producer
	Consumer message.Consumer
	Kafka    bool
}

// Close releases the underlying event bus (DI lifecycle).
func (b *busBundle) Close() error { return b.Producer.Close() }

// RegisterAll wires every component into the container in dependency order:
// infrastructure → factories → services → handlers → workers.
func RegisterAll(c *di.Container, cfg *config.Config, opts Options) error {
	// 1. Infrastructure
	if err := c.RegisterSingleton("logger", func(*di.Container) (any, error) { return slog.Default(), nil }); err != nil {
		return err
	}
	if err := c.RegisterSingleton("db", func(*di.Container) (any, error) {
		d, err := database.Open(cfg.DatabaseURL, database.PoolConfig{
			MaxOpenConns:    cfg.DBMaxOpenConns,
			MaxIdleConns:    cfg.DBMaxIdleConns,
			ConnMaxLifetime: cfg.DBConnMaxLifetime,
			ConnMaxIdleTime: cfg.DBConnMaxIdleTime,
		})
		if err != nil {
			return nil, err
		}
		if err := database.Migrate(d.Gorm()); err != nil {
			return nil, err
		}
		return d, nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("redis", func(*di.Container) (any, error) {
		return redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("limiter", func(cc *di.Container) (any, error) {
		return cache.NewRedisLimiter(cc.MustResolve("redis").(*redis.Client)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("cache.kv", func(cc *di.Container) (any, error) {
		return cache.NewKV(cc.MustResolve("redis").(*redis.Client)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("bus", func(*di.Container) (any, error) {
		if kafkaBus, err := message.NewKafkaEventBus(cfg.KafkaAddr); err == nil {
			return &busBundle{Producer: kafkaBus, Consumer: kafkaBus, Kafka: true}, nil
		}
		slog.Warn("kafka unreachable; deployment worker disabled", "addr", cfg.KafkaAddr)
		mem := message.NewMemoryEventBus()
		return &busBundle{Producer: mem, Consumer: mem, Kafka: false}, nil
	}); err != nil {
		return err
	}
	// API-only: the data-plane client + agentic loop. A worker process never
	// builds these.
	if cfg.Services.API {
		if err := c.RegisterSingleton("inference.client", func(*di.Container) (any, error) {
			return infrainf.NewClient(opts.InferenceAddr)
		}); err != nil {
			return err
		}
		if err := c.RegisterSingleton("batch.scheduler", func(cc *di.Container) (any, error) {
			ic := cc.MustResolve("inference.client").(*infrainf.Client)
			s := infrainf.NewBatchScheduler(ic)
			if opts.MaxConcurrent > 0 {
				s.SetMaxBatchSize(opts.MaxConcurrent)
			}
			s.SetMaxInFlight(cfg.InferenceMaxInFlightBatches)
			return s, nil
		}); err != nil {
			return err
		}
		if err := c.RegisterSingleton("tool.executor", func(*di.Container) (any, error) {
			if cfg.Tools.Executor == "docker" {
				if _, err := exec.LookPath("docker"); err != nil {
					slog.Warn("tools.executor=docker but the docker CLI is not on PATH; tool calls will fail",
						"err", err)
				}
				return inferencesvc.NewDockerToolExecutor(opts.WorkDir, cfg.Tools.DockerImage), nil
			}
			return inferencesvc.NewLocalToolExecutor(opts.WorkDir), nil
		}); err != nil {
			return err
		}
		if err := c.RegisterSingleton("inference.loop", func(cc *di.Container) (any, error) {
			return inferencesvc.NewLoop(cc.MustResolve("batch.scheduler").(*infrainf.BatchScheduler),
				cc.MustResolve("tool.executor").(inferencesvc.ToolExecutor)), nil
		}); err != nil {
			return err
		}
	}

	// 2. Services (always registered; lazy, so a disabled role never builds them)
	if err := c.RegisterSingleton("iam", func(cc *di.Container) (any, error) {
		return iam.NewServiceFromGorm(cc.MustResolve("db").(*database.DB).Gorm()), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("iam.auth", func(cc *di.Container) (any, error) {
		return iam.NewAuthServiceWithCache(cc.MustResolve("iam").(*iam.Service),
			[]byte(cfg.JWTSecret), 8*time.Hour,
			cc.MustResolve("cache.kv").(*cache.KV)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("iam.authenticator", func(cc *di.Container) (any, error) {
		return iam.NewAuthenticator([]byte(cfg.JWTSecret), cc.MustResolve("iam.auth").(*iam.AuthService)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("serving", func(cc *di.Container) (any, error) {
		return serving.NewServiceFromGorm(cc.MustResolve("db").(*database.DB).Gorm()), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("usage", func(cc *di.Container) (any, error) {
		g := cc.MustResolve("db").(*database.DB).Gorm()
		return usage.NewService(usage.NewRepositories(g)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("billing", func(cc *di.Container) (any, error) {
		g := cc.MustResolve("db").(*database.DB).Gorm()
		return billing.NewService(billing.NewRepositories(g), billing.Config{
			Mode:             cfg.Billing.Mode,
			Currency:         cfg.Billing.Currency,
			InitialAllowance: cfg.Billing.InitialAllowance,
			ReservationTTL:   cfg.Billing.ReservationTTL,
			Log:              slog.Default(),
		}, billing.MockProvider{}), nil
	}); err != nil {
		return err
	}
	if cfg.Services.API {
		if err := c.RegisterSingleton("inference.manager", func(cc *di.Container) (any, error) {
			g := cc.MustResolve("db").(*database.DB).Gorm()
			return inferencesvc.NewManagerWithStore(inferencesvc.NewGormStore(g)), nil
		}); err != nil {
			return err
		}
		// Transactional outbox (Phase 6): API nodes write outbox rows and run the
		// publisher; worker nodes neither write nor drain it.
		if err := c.RegisterSingleton("outbox.store", func(cc *di.Container) (any, error) {
			return outbox.NewStore(cc.MustResolve("db").(*database.DB).Gorm()), nil
		}); err != nil {
			return err
		}
		if err := c.RegisterSingleton("outbox.publisher", func(cc *di.Container) (any, error) {
			b := cc.MustResolve("bus").(*busBundle)
			return outbox.NewPublisher(cc.MustResolve("outbox.store").(*outbox.Store), b.Producer, slog.Default()), nil
		}); err != nil {
			return err
		}
		// Usage rollup: API nodes fold new usage_events into usage_daily. The
		// watermark row serialises concurrent rollers, so every replica is safe.
		if err := c.RegisterSingleton("usage.roller", func(cc *di.Container) (any, error) {
			g := cc.MustResolve("db").(*database.DB).Gorm()
			return usage.NewRoller(usage.NewRepositories(g).Aggregates, slog.Default()), nil
		}); err != nil {
			return err
		}
		// Billing reaper: releases holds whose lease lapsed (crash safety).
		if cfg.Services.Billing {
			if err := c.RegisterSingleton("billing.reaper", func(cc *di.Container) (any, error) {
				return billing.NewReaper(cc.MustResolve("billing").(*billing.Service), slog.Default(), cfg.Billing.ReaperInterval), nil
			}); err != nil {
				return err
			}
		}
	}

	// 3. Workers (started conditionally by App.Run)
	if cfg.Services.Worker {
		if err := c.RegisterSingleton("deployment.worker", func(cc *di.Container) (any, error) {
			b := cc.MustResolve("bus").(*busBundle)
			return serving.NewWorker(
				cc.MustResolve("serving").(*serving.Service),
				serving.NewWorkerAdapter(opts.InferenceAddr),
				serving.NewMockComputeProvider(),
				b.Producer, b.Consumer, slog.Default(),
			), nil
		}); err != nil {
			return err
		}
	}

	// 4. HTTP handlers + routes (API nodes only)
	if !cfg.Services.API {
		return nil
	}
	if err := c.RegisterSingleton("http.handler", func(cc *di.Container) (any, error) {
		uiDir := resolveUIDir(opts.UIDir)
		slog.Info("ui directory", "dir", uiDir)
		return inferencesvc.NewHandler(
			cc.MustResolve("inference.manager").(*inferencesvc.Manager),
			cc.MustResolve("inference.loop").(*inferencesvc.Loop),
			uiDir,
			cc.MustResolve("iam.authenticator").(*iam.Authenticator),
			deploymentResolver{svc: cc.MustResolve("serving").(*serving.Service)},
			cc.MustResolve("usage").(*usage.Service),
			billingGateArg(cc, cfg),
			cc.MustResolve("limiter").(*cache.RedisLimiter),
			cfg.RateLimitRPM, cfg.RateLimitConcurrency,
		), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("http.iam", func(cc *di.Container) (any, error) {
		return iam.NewHandler(
			cc.MustResolve("iam").(*iam.Service),
			cc.MustResolve("iam.auth").(*iam.AuthService),
			cc.MustResolve("iam.authenticator").(*iam.Authenticator),
		), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("http.serving", func(cc *di.Container) (any, error) {
		return serving.NewHandler(
			cc.MustResolve("serving").(*serving.Service),
			cc.MustResolve("iam.authenticator").(*iam.Authenticator),
			cc.MustResolve("outbox.store").(*outbox.Store),
		), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("http.usage", func(cc *di.Container) (any, error) {
		return usage.NewHandler(
			cc.MustResolve("usage").(*usage.Service),
			cc.MustResolve("iam.authenticator").(*iam.Authenticator),
		), nil
	}); err != nil {
		return err
	}
	if cfg.Services.Billing {
		if err := c.RegisterSingleton("http.billing", func(cc *di.Container) (any, error) {
			return billing.NewHandler(
				cc.MustResolve("billing").(*billing.Service),
				cc.MustResolve("iam.authenticator").(*iam.Authenticator),
			), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// billingGateArg returns the inference billing gate, or nil when billing is
// disabled or in off mode.
func billingGateArg(c *di.Container, cfg *config.Config) inferencesvc.BillingGate {
	if !cfg.Services.Billing || cfg.Billing.Mode == billing.ModeOff {
		return nil
	}
	return billingGate{svc: c.MustResolve("billing").(*billing.Service)}
}

// resolveUIDir reproduces the current auto-detect (ui/ then ../ui).
func resolveUIDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat("ui"); err == nil {
		return "ui"
	}
	if _, err := os.Stat("../ui"); err == nil {
		return "../ui"
	}
	return ""
}
