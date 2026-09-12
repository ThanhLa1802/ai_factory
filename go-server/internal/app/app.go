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
	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/internal/services/serving"
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
	for _, name := range []string{
		"db", "iam", "iam.auth", "iam.authenticator", "serving", "usage", "bus", "inference.manager",
		"inference.loop", "http.handler", "http.iam", "http.serving", "http.usage",
	} {
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
	e.GET("/metrics", gin.WrapH(observability.MetricsHandler()))
	return e
}

// Run seeds, starts the worker (if Kafka is up), serves HTTP, and blocks until
// SIGINT/SIGTERM, then shuts the container down.
func (a *App) Run() error {
	ctx := context.Background()
	iamSvc := a.container.MustResolve("iam").(*iam.Service)
	servingSvc := a.container.MustResolve("serving").(*serving.Service)
	if err := seedAdmin(ctx, iamSvc); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := seedDemo(ctx, iamSvc, servingSvc); err != nil {
		a.log.Warn("seed demo deployment", "err", err)
	}

	if b := a.container.MustResolve("bus").(*busBundle); b.Kafka {
		w := a.container.MustResolve("deployment.worker").(*serving.Worker)
		if err := w.Run(ctx); err != nil {
			return fmt.Errorf("deployment worker: %w", err)
		}
		a.log.Info("deployment worker started (async deploy)")
	}

	engine := NewHTTPHandler(a.container)
	server := &http.Server{Addr: fmt.Sprintf(":%d", a.port), Handler: engine}

	shutdownDone := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		a.log.Info("shutting down")
		server.Close()
		_ = a.container.Shutdown(context.Background())
		_ = a.container.Close()
		close(shutdownDone)
	}()

	a.log.Info("server listening", "addr", fmt.Sprintf(":%d", a.port))
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	<-shutdownDone
	return nil
}
