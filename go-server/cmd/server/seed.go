package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
)

// seedAdmin creates the default platform admin + a demo tenant if none exist.
func seedAdmin(ctx context.Context, cp *controlplane.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	username := envOr("AI_FACTORY_ADMIN_USER", "admin")
	password := envOr("AI_FACTORY_ADMIN_PASSWORD", "admin1234")
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")

	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("list tenants for seed: %w", err)
	}
	if len(tenants) > 0 {
		return nil
	}
	tenant, err := cp.CreateTenant(ctx, tenantName)
	if err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("seed hash: %w", err)
	}
	if _, err := cp.CreateUser(ctx, username, username+"@localhost", hash, auth.RolePlatformAdmin, tenant.ID); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	log.Printf("seeded tenant=%s admin=%s (password in AI_FACTORY_ADMIN_PASSWORD or default)", tenantName, username)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
