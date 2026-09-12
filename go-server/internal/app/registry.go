package app

import (
	"log/slog"
	"os"
	"time"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/api"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/ai-factory/go-server/internal/runtime"
	"github.com/ai-factory/go-server/internal/session"
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
		d, err := database.Open(cfg.DatabaseURL)
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
	if err := c.RegisterSingleton("inference.client", func(*di.Container) (any, error) {
		return inference.NewClient(opts.InferenceAddr)
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("batch.scheduler", func(cc *di.Container) (any, error) {
		ic := cc.MustResolve("inference.client").(*inference.Client)
		s := inference.NewBatchScheduler(ic)
		if opts.MaxConcurrent > 1 {
			s.SetMaxBatchSize(opts.MaxConcurrent)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("tool.executor", func(*di.Container) (any, error) {
		return agent.NewLocalToolExecutor(opts.WorkDir), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("agent.loop", func(cc *di.Container) (any, error) {
		return agent.NewLoop(cc.MustResolve("batch.scheduler").(*inference.BatchScheduler),
			cc.MustResolve("tool.executor").(*agent.LocalToolExecutor)), nil
	}); err != nil {
		return err
	}

	// 2. Services
	if err := c.RegisterSingleton("controlplane", func(cc *di.Container) (any, error) {
		return controlplane.NewServiceFromGorm(cc.MustResolve("db").(*database.DB).Gorm()), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("auth", func(cc *di.Container) (any, error) {
		return auth.NewService(cc.MustResolve("controlplane").(*controlplane.Service),
			[]byte(cfg.JWTSecret), 8*time.Hour), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("session.manager", func(cc *di.Container) (any, error) {
		g := cc.MustResolve("db").(*database.DB).Gorm()
		return session.NewManagerWithStore(session.NewGormStore(g)), nil
	}); err != nil {
		return err
	}

	// 3. Workers (started conditionally by App.Run)
	if err := c.RegisterSingleton("deployment.worker", func(cc *di.Container) (any, error) {
		b := cc.MustResolve("bus").(*busBundle)
		return runtime.NewWorker(
			cc.MustResolve("controlplane").(*controlplane.Service),
			runtime.NewWorkerAdapter(opts.InferenceAddr),
			runtime.NewMockComputeProvider(),
			b.Producer, b.Consumer, slog.Default(),
		), nil
	}); err != nil {
		return err
	}

	// 4. HTTP handlers + routes
	if err := c.RegisterSingleton("http.handler", func(cc *di.Container) (any, error) {
		uiDir := resolveUIDir(opts.UIDir)
		slog.Info("ui directory", "dir", uiDir)
		return api.NewHandler(
			cc.MustResolve("session.manager").(*session.Manager),
			cc.MustResolve("agent.loop").(*agent.Loop),
			uiDir,
			cc.MustResolve("auth").(*auth.Service),
			[]byte(cfg.JWTSecret),
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("limiter").(*cache.RedisLimiter),
			cfg.RateLimitRPM, cfg.RateLimitConcurrency,
		), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("http.controlplane", func(cc *di.Container) (any, error) {
		b := cc.MustResolve("bus").(*busBundle)
		return api.NewControlPlaneHandler(
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("auth").(*auth.Service),
			[]byte(cfg.JWTSecret), b.Producer,
		), nil
	}); err != nil {
		return err
	}
	return nil
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
