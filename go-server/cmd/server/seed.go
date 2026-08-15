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

// seedDemo seeds a demo model + READY deployment for the demo tenant so routing
// works without Kafka. Idempotent: skips if a READY deployment already resolves.
func seedDemo(ctx context.Context, cp *controlplane.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return err
	}
	var tenantID string
	for _, t := range tenants {
		if t.Name == tenantName {
			tenantID = t.ID
			break
		}
	}
	if tenantID == "" {
		return nil // no demo tenant yet; nothing to seed
	}
	if _, err := cp.ResolveDeployment(ctx, tenantID, "qwen-3b"); err == nil {
		return nil // already seeded
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return fmt.Errorf("resolve for seed: %w", err)
	}

	model, err := cp.CreateModel(ctx, controlplane.Model{Name: "qwen-3b", Task: "text-generation", Framework: "transformers"})
	if err != nil {
		return fmt.Errorf("seed model: %w", err)
	}
	mv, err := cp.CreateModelVersion(ctx, controlplane.ModelVersion{ModelID: model.ID, Version: "v1", ArtifactURI: "local://qwen-3b"})
	if err != nil {
		return fmt.Errorf("seed model version: %w", err)
	}
	tpl, err := cp.CreateTemplate(ctx, controlplane.ServingTemplate{Name: "transformers", Runtime: "transformers"})
	if err != nil {
		return fmt.Errorf("seed template: %w", err)
	}
	tv, err := cp.CreateTemplateVersion(ctx, controlplane.TemplateVersion{TemplateID: tpl.ID, Version: "v1", Image: "qwen-3b:latest"})
	if err != nil {
		return fmt.Errorf("seed template version: %w", err)
	}
	d, err := cp.CreateDeployment(ctx, controlplane.Deployment{
		TenantID: tenantID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "qwen-3b-prod", Region: "local", DesiredReplicas: 1,
	})
	if err != nil {
		return fmt.Errorf("seed deployment: %w", err)
	}
	for _, to := range []string{controlplane.DeploymentProvisioning, controlplane.DeploymentStarting, controlplane.DeploymentReady} {
		if _, err := cp.TransitionDeployment(ctx, d.ID, to); err != nil {
			return fmt.Errorf("seed transition to %s: %w", to, err)
		}
	}
	log.Printf("seeded demo model qwen-3b + READY deployment %s (tenant %s)", d.ID, tenantID)
	return nil
}
