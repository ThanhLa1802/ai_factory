package app

import (
	"context"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/pkg/di"
	"github.com/google/uuid"
)

// deadAddr is guaranteed to refuse quickly, so resolving a provider that needs
// Postgres or Kafka fails on the connection without touching a live service.
func testRoleCfg(api, worker bool) *config.Config {
	return &config.Config{
		DatabaseURL: "postgres://x:x@127.0.0.1:1/x?sslmode=disable",
		JWTSecret:   "0123456789abcdef",
		KafkaAddr:   "127.0.0.1:1",
		RedisAddr:   "127.0.0.1:1",
		Services:    config.ServicesConfig{API: api, Worker: worker},
	}
}

func assertNotRegistered(t *testing.T, c *di.Container, names ...string) {
	t.Helper()
	for _, name := range names {
		if c.Has(name) {
			t.Errorf("container has %q, want not registered", name)
		}
	}
}

func assertRegistered(t *testing.T, c *di.Container, names ...string) {
	t.Helper()
	for _, name := range names {
		if !c.Has(name) {
			t.Errorf("container missing %q, want registered", name)
		}
	}
}

// TestRegisterAllBothRolesOff: no API + no worker providers are registered, but
// the control-plane services the seed runner needs stay available.
func TestRegisterAllBothRolesOff(t *testing.T) {
	c := di.NewContainer()
	if err := RegisterAll(c, testRoleCfg(false, false), Options{}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	assertNotRegistered(t, c,
		"http.handler", "http.iam", "http.serving", "http.usage",
		"deployment.worker", "inference.client", "batch.scheduler",
		"inference.loop", "inference.manager",
	)
	assertRegistered(t, c, "iam", "serving", "usage")
}

// TestRegisterAllWorkerOnly: a worker process registers the deployment worker
// but no HTTP/inference providers.
func TestRegisterAllWorkerOnly(t *testing.T) {
	c := di.NewContainer()
	if err := RegisterAll(c, testRoleCfg(false, true), Options{}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	assertRegistered(t, c, "deployment.worker")
	assertNotRegistered(t, c, "http.handler", "http.iam", "http.serving", "http.usage", "inference.client")
}

// TestRegisterAllAPIOnly: an API process registers HTTP/inference providers but
// no deployment worker when services.worker=false.
func TestRegisterAllAPIOnly(t *testing.T) {
	c := di.NewContainer()
	if err := RegisterAll(c, testRoleCfg(true, false), Options{}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	assertRegistered(t, c, "http.handler", "http.iam", "http.serving", "http.usage")
	assertNotRegistered(t, c, "deployment.worker")
}

// TestRunSeedE2E seeds an admin + demo tenant through the seed runner against a
// real Postgres (skipped without AI_FACTORY_DATABASE_URL).
func TestRunSeedE2E(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
	}
	ctx := context.Background()
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(db.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	suffix := uuid.NewString()[:8]
	t.Setenv("AI_FACTORY_ADMIN_USER", "seed-"+suffix)
	t.Setenv("AI_FACTORY_DEMO_TENANT", "seed-tenant-"+suffix)
	t.Cleanup(func() {
		_ = db.Gorm().Exec(`DELETE FROM users WHERE username = $1`, "seed-"+suffix)
		_ = db.Gorm().Exec(`DELETE FROM tenants WHERE name = $1`, "seed-tenant-"+suffix)
	})

	cfg := testRoleCfg(false, false)
	cfg.DatabaseURL = dsn
	if err := RunSeed(ctx, cfg); err != nil {
		t.Fatalf("RunSeed: %v", err)
	}

	svc := iam.NewServiceFromGorm(db.Gorm())
	if _, _, err := svc.GetUserByUsername(ctx, "seed-"+suffix); err != nil {
		t.Fatalf("seeded admin not found: %v", err)
	}
}
