package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/api"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/internal/ratelimit"
	"github.com/ai-factory/go-server/internal/runtime"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/redis/go-redis/v9"
)

func main() {
	var (
		httpPort      = flag.Int("port", 8080, "HTTP server port")
		inferenceAddr = flag.String("inference-addr", "localhost:50051", "Python inference worker gRPC address")
		workDir       = flag.String("workdir", ".", "Working directory for tool execution")
		maxConcurrent = flag.Int("max-concurrent", 1, "Max concurrent inference requests")
		uiDir         = flag.String("ui-dir", "", "Directory with standalone UI HTML (default: auto-detect ui/ or ../ui)")
	)
	flag.Parse()

	// Load config from environment + structured logger (JSON slog).
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	observability.SetupLogger(cfg.LogLevel)
	slog.Info("AI Factory Server starting", "http_port", *httpPort, "inference_addr", *inferenceAddr)

	// Control plane persistence (mandatory)
	ctx := context.Background()
	d, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	cp := controlplane.NewService(d.Pool())
	authSvc := auth.NewService(cp, []byte(cfg.JWTSecret), 15*time.Minute)
	if err := seedAdmin(ctx, cp); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
	if err := seedDemo(ctx, cp); err != nil {
		slog.Warn("seed demo deployment", "err", err)
	}

	// Redis-backed rate limiter (fixed-window RPM + concurrency).
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	limiter := ratelimit.NewRedisLimiter(rdb)

	// Events bus (Kafka) — optional. The deployment worker needs it, but the
	// chat/inference path must boot without it: fall back to an in-memory bus
	// (deployments stay PENDING) and warn.
	var producer events.Producer
	var consumer events.Consumer
	if kafkaBus, kafkaErr := events.NewKafkaEventBus(cfg.KafkaAddr); kafkaErr != nil {
		slog.Warn("kafka unreachable; deployment worker disabled", "addr", cfg.KafkaAddr, "err", kafkaErr)
		mem := events.NewMemoryEventBus()
		producer, consumer = mem, mem
	} else {
		defer kafkaBus.Close()
		slog.Info("kafka event bus connected", "addr", cfg.KafkaAddr)
		producer, consumer = kafkaBus, kafkaBus
		worker := runtime.NewWorker(cp, runtime.NewWorkerAdapter(*inferenceAddr), runtime.NewMockComputeProvider(), producer, consumer, slog.Default())
		if err := worker.Run(ctx); err != nil {
			slog.Error("deployment worker", "err", err)
			os.Exit(1)
		}
		slog.Info("deployment worker started (async deploy)")
	}
	cph := api.NewControlPlaneHandler(cp, authSvc, []byte(cfg.JWTSecret), producer)

	// Connect to Python inference worker
	inferenceClient, err := inference.NewClient(*inferenceAddr)
	if err != nil {
		slog.Error("connect inference worker", "addr", *inferenceAddr, "err", err)
		os.Exit(1)
	}
	defer inferenceClient.Close()
	slog.Info("connected to inference worker", "addr", *inferenceAddr)

	// Batch scheduler — collects requests in 100ms windows for GPU batching
	batchScheduler := inference.NewBatchScheduler(inferenceClient)
	if *maxConcurrent > 1 {
		batchScheduler.SetMaxBatchSize(*maxConcurrent)
	}
	slog.Info("batch scheduler ready", "window", inference.DefaultBatchWindow.String(), "max_batch", *maxConcurrent)

	// Tool executor
	toolExecutor := agent.NewLocalToolExecutor(*workDir)
	slog.Info("tool executor ready", "tools", len(toolExecutor.ListTools()))

	// Agentic loop — uses batch scheduler instead of direct inference
	loop := agent.NewLoop(batchScheduler, toolExecutor)

	// Session manager (no longer manages inference queue — batch scheduler handles that)
	sessionMgr := session.NewManager()

	// UI directory — standalone HTML files (không nhúng vào binary)
	dir := *uiDir
	if dir == "" {
		if _, err := os.Stat("ui"); err == nil {
			dir = "ui"
		} else if _, err := os.Stat("../ui"); err == nil {
			dir = "../ui"
		}
	}
	slog.Info("ui directory", "dir", dir)

	// HTTP handler
	handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret), cp, limiter, cfg.RateLimitRPM, cfg.RateLimitConcurrency)

	// Register routes
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	cph.RegisterRoutes(mux)

	// Metrics endpoint (Prometheus) — on the same mux as the API routes.
	mux.Handle("/metrics", observability.MetricsHandler())

	// Middleware chain: CORS trước (cho UI chạy độc lập ở origin khác), rồi trace
	// (root span per request), rồi logging, rồi metrics.
	loggedMux := corsMiddleware(traceMiddleware(loggingMiddleware(metricsMiddleware(mux))))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *httpPort),
		Handler: loggedMux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down")
		server.Close()
	}()

	slog.Info("server listening",
		"addr", fmt.Sprintf(":%d", *httpPort),
		"openai", fmt.Sprintf("http://localhost:%d/v1/chat/completions", *httpPort),
		"health", fmt.Sprintf("http://localhost:%d/health", *httpPort))

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
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
