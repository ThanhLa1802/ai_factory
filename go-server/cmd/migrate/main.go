package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/ai-factory/go-server/internal/app"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
)

// Migration runner: applies pending gormigrate migrations, then exits.
func main() {
	configPath := flag.String("config", "configs/config.yaml", "Path to YAML config (empty to skip)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	observability.SetupLogger(cfg.LogLevel)

	if err := app.RunMigrate(cfg); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")
}
