package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// seedAdmin creates the default platform admin + a demo tenant if the admin is
// missing. Idempotency is keyed on admin existence (not "any tenant exists") so
// a reused DB that has tenants but no seeded admin still gets one.
func seedAdmin(ctx context.Context, cp *controlplane.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	username := envOr("AI_FACTORY_ADMIN_USER", "admin")
	password := envOr("AI_FACTORY_ADMIN_PASSWORD", "admin1234")
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")

	_, _, err := cp.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		return nil // admin already seeded — idempotent
	case errors.Is(err, pgx.ErrNoRows):
		// admin missing — fall through and seed below
	default:
		return fmt.Errorf("look up admin for seed: %w", err)
	}

	// Reuse an existing demo tenant if present; otherwise create it.
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("list tenants for seed: %w", err)
	}
	var tenant *controlplane.Tenant
	for i := range tenants {
		if tenants[i].Name == tenantName {
			tenant = &tenants[i]
			break
		}
	}
	if tenant == nil {
		t, err := cp.CreateTenant(ctx, tenantName)
		if err != nil {
			return fmt.Errorf("seed tenant: %w", err)
		}
		tenant = t
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
