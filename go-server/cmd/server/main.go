package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/internal/session"
)

func main() {
	var (
		httpPort         = flag.Int("port", 8080, "HTTP server port")
		inferenceAddr    = flag.String("inference-addr", "localhost:50051", "Python inference worker gRPC address")
		workDir          = flag.String("workdir", ".", "Working directory for tool execution")
		maxConcurrent     = flag.Int("max-concurrent", 1, "Max concurrent inference requests")
		uiDir             = flag.String("ui-dir", "", "Directory with standalone UI HTML (default: auto-detect ui/ or ../ui)")
	)
	flag.Parse()

	// Load config from environment + structured logger (JSON slog).
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	observability.SetupLogger(cfg.LogLevel)

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("=== AI Factory Server ===")
	log.Printf("HTTP port: %d", *httpPort)
	log.Printf("Inference worker: %s", *inferenceAddr)

	// Control plane persistence (mandatory)
	ctx := context.Background()
	d, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	cp := controlplane.NewService(d.Pool())
	authSvc := auth.NewService(cp, []byte(cfg.JWTSecret), 15*time.Minute)
	cph := api.NewControlPlaneHandler(cp, authSvc, []byte(cfg.JWTSecret))
	if err := seedAdmin(ctx, cp); err != nil {
		log.Fatalf("seed: %v", err)
	}

	// Connect to Python inference worker
	inferenceClient, err := inference.NewClient(*inferenceAddr)
	if err != nil {
		log.Fatalf("Failed to connect to inference worker: %v", err)
	}
	defer inferenceClient.Close()
	log.Println("Connected to inference worker")

	// Batch scheduler — collects requests in 100ms windows for GPU batching
	batchScheduler := inference.NewBatchScheduler(inferenceClient)
	if *maxConcurrent > 1 {
		batchScheduler.SetMaxBatchSize(*maxConcurrent)
	}
	log.Printf("Batch scheduler ready: window=%v, max_batch=%d",
		inference.DefaultBatchWindow, *maxConcurrent)

	// Tool executor
	toolExecutor := agent.NewLocalToolExecutor(*workDir)
	log.Printf("Tool executor ready with %d tools", len(toolExecutor.ListTools()))

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
	log.Printf("UI directory: %s", dir)

	// HTTP handler
	handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret))

	// Register routes
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	cph.RegisterRoutes(mux)

	// Metrics endpoint (Prometheus) — on the same mux as the API routes.
	mux.Handle("/metrics", observability.MetricsHandler())

	// Middleware: CORS trước (cho UI chạy độc lập ở origin khác), rồi logging, rồi metrics
	loggedMux := corsMiddleware(loggingMiddleware(metricsMiddleware(mux)))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *httpPort),
		Handler: loggedMux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		server.Close()
	}()

	log.Printf("Server listening on http://localhost:%d", *httpPort)
	log.Printf("  Anthropic: POST http://localhost:%d/v1/messages", *httpPort)
	log.Printf("  OpenAI:    POST http://localhost:%d/v1/chat/completions", *httpPort)
	log.Printf("  Health:    GET  http://localhost:%d/health", *httpPort)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	log.Println("Server stopped.")
}

// loggingMiddleware logs each request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[http] %s %s", r.Method, r.URL.Path)
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

// metricsMiddleware ghi status + duration của mỗi request vào Prometheus.
// Labels tenant/deployment/model/region (serving-domain, chưa resolve ở tầng HTTP)
// để trống; status ghi HTTP status code thật.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		status := strconv.Itoa(rec.status)
		observability.HTTPRequestsTotal.WithLabelValues("", "", "", "", status).Inc()
		observability.RequestDurationSeconds.WithLabelValues("", "", "", "", status).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder bắt status code thực tế của handler (mặc định 200 khi WriteHeader không được gọi).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush delegating xuống writer gốc nếu nó hỗ trợ (SSE streaming).
// http.ResponseWriter là interface không khai báo Flush, nên nếu không
// override, statusRecorder không thỏa http.Flusher → NewSSEWriter fail
// với "streaming not supported" trên cả /v1/messages lẫn /v1/chat/completions.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
