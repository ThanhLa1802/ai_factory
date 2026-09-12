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

// Worker node: consumes deployment events and drives the deployment state
// machine, headless (no HTTP). Uses the same image/codebase as the API node,
// selected by the services.worker role.
func main() {
	var (
		configPath    = flag.String("config", "configs/config.yaml", "Path to YAML config (empty to skip)")
		inferenceAddr = flag.String("inference-addr", "localhost:50051", "Python inference worker gRPC address")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	cfg.Services = config.ServicesConfig{API: false, Worker: true}
	observability.SetupLogger(cfg.LogLevel)
	slog.Info("AI Factory Worker starting", "inference_addr", *inferenceAddr)

	container := di.NewContainer()
	if err := app.RegisterAll(container, cfg, app.Options{InferenceAddr: *inferenceAddr}); err != nil {
		slog.Error("register", "err", err)
		os.Exit(1)
	}
	application, err := app.NewAppFromContainer(container, cfg, 0)
	if err != nil {
		slog.Error("boot", "err", err)
		os.Exit(1)
	}
	if err := application.Run(); err != nil {
		slog.Error("run", "err", err)
		os.Exit(1)
	}
	slog.Info("worker stopped")
}
