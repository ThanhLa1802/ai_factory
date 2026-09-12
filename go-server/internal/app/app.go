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
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/internal/runtime"
	"github.com/ai-factory/go-server/pkg/di"
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

	mux := http.NewServeMux()
	a.container.MustResolve("http.handler").(interface{ RegisterRoutes(*http.ServeMux) }).RegisterRoutes(mux)
	a.container.MustResolve("http.controlplane").(interface{ RegisterRoutes(*http.ServeMux) }).RegisterRoutes(mux)
	mux.Handle("/metrics", observability.MetricsHandler())

	handler := corsMiddleware(traceMiddleware(loggingMiddleware(metricsMiddleware(mux))))
	server := &http.Server{Addr: fmt.Sprintf(":%d", a.port), Handler: handler}

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

// loggingMiddleware logs each request as a structured JSON line.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("http request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware cho phép UI tĩnh (mở file:// hoặc serve ở port khác) gọi API.
// Dev/demo nên mở toàn bộ origin; chặn preflight OPTIONS.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-session-id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// traceMiddleware continues an inbound W3C traceparent (if any) and starts a
// root span for the request. The span — and therefore the trace — ends when the
// handler returns. See observability.StartSpan/End (A6 — traces).
func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := observability.ExtractTraceparent(r.Context(), r.Header.Get(observability.TraceparentHeader))
		ctx, span := observability.StartSpan(ctx, "http "+r.Method+" "+r.URL.Path)
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// metricsMiddleware ghi status + duration của mỗi request vào Prometheus.
// Labels tenant/deployment/model/region được handler set trên statusRecorder
// sau khi route resolve (xem observability.RouteLabelSetter); nếu handler không
// set thì chúng để trống. Status ghi HTTP status code thật.
// Đồng thời theo dõi số request đang phục vụ (serving_inflight_requests).
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		observability.IncInflight()
		defer observability.DecInflight()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		status := strconv.Itoa(rec.status)
		observability.HTTPRequestsTotal.WithLabelValues(rec.tenant, rec.deployment, rec.model, rec.region, status).Inc()
		observability.RequestDurationSeconds.WithLabelValues(rec.tenant, rec.deployment, rec.model, rec.region, status).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder bắt status code thực tế của handler (mặc định 200 khi WriteHeader không được gọi)
// và lưu serving-domain labels (tenant/deployment/model/region) mà handler set sau route resolve.
type statusRecorder struct {
	http.ResponseWriter
	status     int
	tenant     string
	deployment string
	model      string
	region     string
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// SetRouteLabels implements observability.RouteLabelSetter — handler gọi sau
// khi resolve deployment để metrics ghi nhãn theo route thật.
func (r *statusRecorder) SetRouteLabels(tenant, deployment, model, region string) {
	r.tenant, r.deployment, r.model, r.region = tenant, deployment, model, region
}

// Flush delegating xuống writer gốc nếu nó hỗ trợ (SSE streaming).
// http.ResponseWriter là interface không khai báo Flush, nên nếu không
// override, statusRecorder không thỏa http.Flusher → NewSSEWriter fail
// với "streaming not supported" trên /v1/chat/completions.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
