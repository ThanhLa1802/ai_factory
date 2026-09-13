package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/ai-factory/go-server/internal/infrastructure/outbox"
	"github.com/ai-factory/go-server/internal/services/billing"
	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/internal/services/serving"
	"github.com/ai-factory/go-server/internal/services/usage"
	"github.com/ai-factory/go-server/pkg/di"
	"github.com/gin-gonic/gin"
)

// App owns the runtime lifecycle: seed → start worker → serve HTTP → shutdown.
type App struct {
	cfg       *config.Config
	container *di.Container
	log       *slog.Logger
	port      int
}

// NewAppFromContainer force-resolves the critical singletons so a bad config
// fails fast at boot rather than at first request.
func NewAppFromContainer(c *di.Container, cfg *config.Config, port int) (*App, error) {
	// Force-resolve only the singletons the enabled roles need, so a worker
	// process fails fast on DB/bus/worker and never builds the HTTP stack.
	names := []string{"db", "bus", "serving"}
	if cfg.Services.API {
		names = append(names,
			"iam", "iam.auth", "iam.authenticator", "usage", "billing",
			"inference.manager", "inference.loop",
			"outbox.store", "outbox.publisher", "usage.roller",
			"http.handler", "http.iam", "http.serving", "http.usage",
		)
		if cfg.Services.Billing {
			names = append(names, "billing.reaper", "http.billing")
		}
	}
	if cfg.Services.Worker {
		names = append(names, "deployment.worker")
	}
	for _, name := range names {
		if _, err := c.Resolve(name); err != nil {
			return nil, err
		}
	}
	return &App{cfg: cfg, container: c, log: slog.Default(), port: port}, nil
}

// NewHTTPHandler builds the Gin engine: global middleware + mounted routes.
func NewHTTPHandler(c *di.Container) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.HandleMethodNotAllowed = true
	e.Use(middleware.Recovery, middleware.CORS, middleware.Trace, middleware.Logging, middleware.Metrics)
	c.MustResolve("http.handler").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	c.MustResolve("http.iam").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	c.MustResolve("http.serving").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	c.MustResolve("http.usage").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	if _, err := c.Resolve("http.billing"); err == nil {
		c.MustResolve("http.billing").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	}
	e.GET("/metrics", gin.WrapH(observability.MetricsHandler()))
	return e
}

// Run starts the roles enabled in config, blocks until SIGINT/SIGTERM, then
// shuts the container down. API nodes seed + serve HTTP; worker nodes consume
// deployment events headlessly (no HTTP).
func (a *App) Run() error {
	ctx := context.Background()
	if a.cfg.Services.API {
		if err := a.seed(ctx); err != nil {
			return err
		}
		a.container.MustResolve("outbox.publisher").(*outbox.Publisher).Start(ctx)
		a.container.MustResolve("usage.roller").(*usage.Roller).Start(ctx)
		if _, err := a.container.Resolve("billing.reaper"); err == nil {
			a.container.MustResolve("billing.reaper").(*billing.Reaper).Start(ctx)
		}
	}
	if a.cfg.Services.Worker {
		if err := a.startWorker(ctx); err != nil {
			return err
		}
	}

	var server *http.Server
	if a.cfg.Services.API {
		server = &http.Server{
			Addr:              fmt.Sprintf(":%d", a.port),
			Handler:           NewHTTPHandler(a.container),
			ReadHeaderTimeout: a.cfg.HTTPReadHeaderTimeout,
			IdleTimeout:       a.cfg.HTTPIdleTimeout,
			MaxHeaderBytes:    1 << 20,
			// WriteTimeout stays 0: SSE responses are long-lived streams and a
			// wall-clock write deadline would sever them mid-generation.
		}
	}

	shutdownDone := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		a.log.Info("shutting down")
		if server != nil {
			server.Close()
		}
		_ = a.container.Shutdown(context.Background())
		_ = a.container.Close()
		close(shutdownDone)
	}()

	if server == nil {
		a.log.Info("worker running; waiting for shutdown signal")
		<-shutdownDone
		return nil
	}
	a.log.Info("server listening", "addr", fmt.Sprintf(":%d", a.port))
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	<-shutdownDone
	return nil
}

// seed creates the platform admin + demo tenant/deployment (best-effort demo).
func (a *App) seed(ctx context.Context) error {
	iamSvc := a.container.MustResolve("iam").(*iam.Service)
	servingSvc := a.container.MustResolve("serving").(*serving.Service)
	if err := seedAdmin(ctx, iamSvc); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := seedDemo(ctx, iamSvc, servingSvc); err != nil {
		a.log.Warn("seed demo deployment", "err", err)
	}
	if a.cfg.Services.Billing {
		billingSvc := a.container.MustResolve("billing").(*billing.Service)
		if err := seedBilling(ctx, iamSvc, billingSvc); err != nil {
			a.log.Warn("seed billing", "err", err)
		}
	}
	return nil
}

// startWorker subscribes the deployment worker when Kafka is reachable; an
// unreachable Kafka degrades to a warning (deployments stay PENDING).
func (a *App) startWorker(ctx context.Context) error {
	b := a.container.MustResolve("bus").(*busBundle)
	if !b.Kafka {
		a.log.Warn("kafka unreachable; deployment worker disabled")
		return nil
	}
	w := a.container.MustResolve("deployment.worker").(*serving.Worker)
	if err := w.Run(ctx); err != nil {
		return fmt.Errorf("deployment worker: %w", err)
	}
	a.log.Info("deployment worker started (async deploy)")
	return nil
}
