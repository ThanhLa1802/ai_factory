package app

import (
	"context"
	"fmt"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/internal/services/serving"
	"github.com/ai-factory/go-server/pkg/di"
)

// RunMigrate opens the database and applies pending gormigrate migrations, then
// returns. It starts no server and no worker (cmd/migrate).
func RunMigrate(cfg *config.Config) error {
	d, err := database.Open(cfg.DatabaseURL, database.PoolConfig{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnMaxLifetime,
		ConnMaxIdleTime: cfg.DBConnMaxIdleTime,
	})
	if err != nil {
		return err
	}
	defer d.Close()
	return database.Migrate(d.Gorm())
}

// RunSeed builds a minimal container (DB + control-plane services, neither API
// nor worker role) and runs the admin + demo seeders once (cmd/seed).
func RunSeed(ctx context.Context, cfg *config.Config) error {
	seedCfg := *cfg
	seedCfg.Services = config.ServicesConfig{}
	c := di.NewContainer()
	defer c.Close()
	if err := RegisterAll(c, &seedCfg, Options{}); err != nil {
		return err
	}
	iamSvc := c.MustResolve("iam").(*iam.Service)
	servingSvc := c.MustResolve("serving").(*serving.Service)
	if err := seedAdmin(ctx, iamSvc); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := seedDemo(ctx, iamSvc, servingSvc); err != nil {
		return fmt.Errorf("seed demo: %w", err)
	}
	return nil
}
