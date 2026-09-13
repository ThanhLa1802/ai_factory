package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/ai-factory/go-server/internal/app"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/ai-factory/go-server/pkg/di"
)

func main() {
	var (
		httpPort      = flag.Int("port", 8080, "HTTP server port")
		configPath    = flag.String("config", "configs/config.yaml", "Path to YAML config (empty to skip)")
		inferenceAddr = flag.String("inference-addr", "localhost:50051", "Python inference worker gRPC address")
		workDir       = flag.String("workdir", ".", "Working directory for tool execution")
		maxConcurrent = flag.Int("max-concurrent", 0, "Batch size for inference (0 = default 4)")
		uiDir         = flag.String("ui-dir", "", "Directory with standalone UI HTML (default: auto-detect)")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	observability.SetupLogger(cfg.LogLevel)
	slog.Info("AI Factory Server starting", "http_port", *httpPort, "inference_addr", *inferenceAddr)

	container := di.NewContainer()
	if err := app.RegisterAll(container, cfg, app.Options{
		InferenceAddr: *inferenceAddr,
		WorkDir:       *workDir,
		MaxConcurrent: *maxConcurrent,
		UIDir:         *uiDir,
	}); err != nil {
		slog.Error("register", "err", err)
		os.Exit(1)
	}
	application, err := app.NewAppFromContainer(container, cfg, *httpPort)
	if err != nil {
		slog.Error("boot", "err", err)
		os.Exit(1)
	}
	if err := application.Run(); err != nil {
		slog.Error("run", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
