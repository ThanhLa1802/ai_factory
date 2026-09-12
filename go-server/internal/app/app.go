package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/ai-factory/go-server/internal/runtime"
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
		"db", "controlplane", "auth", "bus", "session.manager",
		"agent.loop", "http.handler", "http.controlplane",
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
	e.Use(recoveryMiddleware, corsMiddleware, traceMiddleware, loggingMiddleware, metricsMiddleware)
	c.MustResolve("http.handler").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	c.MustResolve("http.controlplane").(interface{ RegisterRoutes(*gin.Engine) }).RegisterRoutes(e)
	e.GET("/metrics", gin.WrapH(observability.MetricsHandler()))
	return e
}

// Run seeds, starts the worker (if Kafka is up), serves HTTP, and blocks until
// SIGINT/SIGTERM, then shuts the container down.
func (a *App) Run() error {
	ctx := context.Background()
	cp := a.container.MustResolve("controlplane").(*controlplane.Service)
	if err := seedAdmin(ctx, cp); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := seedDemo(ctx, cp); err != nil {
		a.log.Warn("seed demo deployment", "err", err)
	}

	if b := a.container.MustResolve("bus").(*busBundle); b.Kafka {
		w := a.container.MustResolve("deployment.worker").(*runtime.Worker)
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

// recoveryMiddleware converts a panic into a 500 instead of crashing the server.
func recoveryMiddleware(c *gin.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered", "err", r, "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{"code": "INTERNAL_ERROR", "message": "internal error"},
			})
		}
	}()
	c.Next()
}

// loggingMiddleware logs each request as a structured JSON line.
func loggingMiddleware(c *gin.Context) {
	slog.Info("http request", "method", c.Request.Method, "path", c.Request.URL.Path)
	c.Next()
}

// corsMiddleware cho phép UI tĩnh (mở file:// hoặc serve ở port khác) gọi API.
// Dev/demo nên mở toàn bộ origin; chặn preflight OPTIONS.
func corsMiddleware(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, x-session-id")
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}

// traceMiddleware continues an inbound W3C traceparent (if any) and starts a
// root span for the request. The span — and therefore the trace — ends when the
// handler returns. See observability.StartSpan/End (A6 — traces).
func traceMiddleware(c *gin.Context) {
	ctx := observability.ExtractTraceparent(c.Request.Context(), c.GetHeader(observability.TraceparentHeader))
	ctx, span := observability.StartSpan(ctx, "http "+c.Request.Method+" "+c.Request.URL.Path)
	defer span.End()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

// metricsMiddleware ghi status + duration của mỗi request vào Prometheus.
// Labels tenant/deployment/model/region được handler set trên metricWriter
// sau khi route resolve (xem observability.RouteLabelSetter); nếu handler không
// set thì chúng để trống. Status ghi HTTP status code thật.
// Đồng thời theo dõi số request đang phục vụ (serving_inflight_requests).
func metricsMiddleware(c *gin.Context) {
	start := time.Now()
	observability.IncInflight()
	mw := &metricWriter{ResponseWriter: c.Writer, status: http.StatusOK}
	c.Writer = mw
	c.Next()
	observability.DecInflight()
	status := strconv.Itoa(mw.status)
	observability.HTTPRequestsTotal.WithLabelValues(mw.tenant, mw.deployment, mw.model, mw.region, status).Inc()
	observability.RequestDurationSeconds.WithLabelValues(mw.tenant, mw.deployment, mw.model, mw.region, status).Observe(time.Since(start).Seconds())
}

// metricWriter bắt status code thực tế của handler (mặc định 200 khi WriteHeader không được gọi)
// và lưu serving-domain labels (tenant/deployment/model/region) mà handler set sau route resolve.
// Nó nhúng gin.ResponseWriter nên vẫn thỏa http.Flusher (SSE streaming).
type metricWriter struct {
	gin.ResponseWriter
	status     int
	tenant     string
	deployment string
	model      string
	region     string
}

func (w *metricWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// SetRouteLabels implements observability.RouteLabelSetter — handler gọi sau
// khi resolve deployment để metrics ghi nhãn theo route thật.
func (w *metricWriter) SetRouteLabels(tenant, deployment, model, region string) {
	w.tenant, w.deployment, w.model, w.region = tenant, deployment, model, region
}
