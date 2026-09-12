package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/ai-factory/go-server/internal/app"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
)

// Seed runner: creates the platform admin + demo tenant/model/deployment, then
// exits. Idempotent; respects AI_FACTORY_SKIP_SEED=1.
func main() {
	configPath := flag.String("config", "configs/config.yaml", "Path to YAML config (empty to skip)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	observability.SetupLogger(cfg.LogLevel)

	if err := app.RunSeed(context.Background(), cfg); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
	slog.Info("seed complete")
}
