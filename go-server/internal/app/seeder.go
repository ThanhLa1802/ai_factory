package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/ai-factory/go-server/internal/services/billing"
	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/internal/services/serving"
)

// seedAdmin creates the default platform admin + a demo tenant if the admin is
// missing. Idempotency is keyed on admin existence (not "any tenant exists") so
// a reused DB that has tenants but no seeded admin still gets one.
func seedAdmin(ctx context.Context, iamSvc *iam.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	username := envOr("AI_FACTORY_ADMIN_USER", "admin")
	password := envOr("AI_FACTORY_ADMIN_PASSWORD", "admin1234")
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")

	_, _, err := iamSvc.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		return nil // admin already seeded — idempotent
	case errors.Is(err, iam.ErrNotFound):
		// admin missing — fall through and seed below
	default:
		return fmt.Errorf("look up admin for seed: %w", err)
	}

	// Reuse an existing demo tenant if present; otherwise create it.
	tenants, err := iamSvc.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("list tenants for seed: %w", err)
	}
	var tenant *iam.Tenant
	for i := range tenants {
		if tenants[i].Name == tenantName {
			tenant = &tenants[i]
			break
		}
	}
	if tenant == nil {
		t, err := iamSvc.CreateTenant(ctx, tenantName)
		if err != nil {
			return fmt.Errorf("seed tenant: %w", err)
		}
		tenant = t
	}
	hash, err := iam.HashPassword(password)
	if err != nil {
		return fmt.Errorf("seed hash: %w", err)
	}
	if _, err := iamSvc.CreateUser(ctx, username, username+"@localhost", hash, iam.RolePlatformAdmin, tenant.ID); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	slog.Info("seeded admin", "tenant", tenantName, "admin", username)
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
func seedDemo(ctx context.Context, iamSvc *iam.Service, cp *serving.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")
	tenants, err := iamSvc.ListTenants(ctx)
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
	if _, err := cp.ResolveDeployment(ctx, tenantID, "qwen3.5-9b"); err == nil {
		return nil // already seeded
	} else if !errors.Is(err, serving.ErrNotFound) {
		return fmt.Errorf("resolve for seed: %w", err)
	}

	model, err := findOrCreateModel(ctx, cp, serving.Model{Name: "qwen3.5-9b", Task: "text-generation", Framework: "llama.cpp"})
	if err != nil {
		return fmt.Errorf("seed model: %w", err)
	}
	mv, err := findOrCreateModelVersion(ctx, cp, model.ID, serving.ModelVersion{Version: "v1", ArtifactURI: "local://qwen3.5-9b"})
	if err != nil {
		return fmt.Errorf("seed model version: %w", err)
	}
	tpl, err := findOrCreateTemplate(ctx, cp, serving.ServingTemplate{Name: "llama-openai", Runtime: "llama.cpp"})
	if err != nil {
		return fmt.Errorf("seed template: %w", err)
	}
	tv, err := findOrCreateTemplateVersion(ctx, cp, tpl.ID, serving.TemplateVersion{Version: "v1", Image: "qwen3.5-9b:latest"})
	if err != nil {
		return fmt.Errorf("seed template version: %w", err)
	}
	d, err := cp.CreateDeployment(ctx, serving.Deployment{
		TenantID: tenantID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "qwen3.5-9b-prod", Region: "local", DesiredReplicas: 1,
	})
	if err != nil {
		return fmt.Errorf("seed deployment: %w", err)
	}
	for _, to := range []string{serving.DeploymentProvisioning, serving.DeploymentStarting, serving.DeploymentReady} {
		if _, err := cp.TransitionDeployment(ctx, d.ID, to); err != nil {
			return fmt.Errorf("seed transition to %s: %w", to, err)
		}
	}
	slog.Info("seeded demo model + READY deployment", "model", "qwen3.5-9b", "deployment", d.ID, "tenant", tenantID)
	return nil
}

// The find-or-create helpers keep seedDemo idempotent across tenants: models and
// templates are global (unique by name/framework), so a second seed against a
// database that already has them must reuse the existing rows instead of
// failing on the unique constraint.

func findOrCreateModel(ctx context.Context, cp *serving.Service, want serving.Model) (*serving.Model, error) {
	models, err := cp.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if models[i].Name == want.Name && models[i].Framework == want.Framework {
			return &models[i], nil
		}
	}
	return cp.CreateModel(ctx, want)
}

func findOrCreateModelVersion(ctx context.Context, cp *serving.Service, modelID string, want serving.ModelVersion) (*serving.ModelVersion, error) {
	if mv, err := cp.GetModelVersion(ctx, modelID, want.Version); err == nil {
		return mv, nil
	} else if !errors.Is(err, serving.ErrNotFound) {
		return nil, err
	}
	want.ModelID = modelID
	return cp.CreateModelVersion(ctx, want)
}

func findOrCreateTemplate(ctx context.Context, cp *serving.Service, want serving.ServingTemplate) (*serving.ServingTemplate, error) {
	templates, err := cp.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for i := range templates {
		if templates[i].Name == want.Name {
			return &templates[i], nil
		}
	}
	return cp.CreateTemplate(ctx, want)
}

func findOrCreateTemplateVersion(ctx context.Context, cp *serving.Service, templateID string, want serving.TemplateVersion) (*serving.TemplateVersion, error) {
	if tv, err := cp.GetTemplateVersion(ctx, templateID, want.Version); err == nil {
		return tv, nil
	} else if !errors.Is(err, serving.ErrNotFound) {
		return nil, err
	}
	want.TemplateID = templateID
	return cp.CreateTemplateVersion(ctx, want)
}

// seedBilling seeds demo model prices and credits the demo tenant's wallet.
// Idempotent: prices upsert; the top-up uses a fixed idempotency key per tenant
// so a repeated seed never credits twice.
func seedBilling(ctx context.Context, iamSvc *iam.Service, billingSvc *billing.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")
	tenants, err := iamSvc.ListTenants(ctx)
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

	for _, p := range []billing.Price{
		{Model: "qwen3.5-9b", PricePerMillionInputTokens: 150_000, PricePerMillionOutputTokens: 600_000},
		{Model: "qwen-3b", PricePerMillionInputTokens: 150_000, PricePerMillionOutputTokens: 600_000},
	} {
		if _, err := billingSvc.UpsertPrice(ctx, p); err != nil {
			return fmt.Errorf("seed price %s: %w", p.Model, err)
		}
	}

	credits := int64(10)
	if v := os.Getenv("AI_FACTORY_DEMO_CREDITS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			credits = n
		}
	}
	if credits > 0 {
		if _, err := billingSvc.TopUp(ctx, tenantID, credits*1_000_000, "seed-demo-"+tenantID); err != nil {
			return fmt.Errorf("seed credits: %w", err)
		}
	}
	slog.Info("seeded billing pricing + demo credits", "tenant", tenantID, "credits", credits)
	return nil
}
