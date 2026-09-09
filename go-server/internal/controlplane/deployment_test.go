package controlplane

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/db"
	"github.com/google/uuid"
)

// TestWorkloadRefAndEndpointIntegration runs against a live Postgres
// (set AI_FACTORY_DATABASE_URL), mirroring users_test.go.
func TestWorkloadRefAndEndpointIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(d.Pool())

	// Use an ephemeral tenant so re-runs never collide on FK constraints.
	tenant, err := s.CreateTenant(ctx, "wl-tenant-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Seed FK parents: models → model_versions and serving_templates → serving_template_versions.
	// deployments has NOT NULL UUID FKs to both; ephemeral names avoid UNIQUE collisions on re-run.
	model, err := s.CreateModel(ctx, Model{
		Name: "wl-model-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}

	mv, err := s.CreateModelVersion(ctx, ModelVersion{
		ModelID: model.ID, Version: "v1", ArtifactURI: "s3://bucket/model",
	})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}

	tpl, err := s.CreateTemplate(ctx, ServingTemplate{
		Name: "wl-template-" + uuid.NewString()[:8], Runtime: "transformers",
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	tv, err := s.CreateTemplateVersion(ctx, TemplateVersion{
		TemplateID: tpl.ID, Version: "v1", Image: "image:latest",
	})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}

	deploy, err := s.CreateDeployment(ctx, Deployment{
		TenantID: tenant.ID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "svc", Region: "us-east-1", DesiredReplicas: 1,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if err := s.SetWorkloadRef(ctx, deploy.ID, "mock-wl-abc"); err != nil {
		t.Fatalf("set workload ref: %v", err)
	}
	got, err := s.GetDeployment(ctx, deploy.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.WorkloadRef != "mock-wl-abc" {
		t.Fatalf("WorkloadRef = %q, want mock-wl-abc", got.WorkloadRef)
	}

	ep, err := s.CreateEndpoint(ctx, deploy.ID, "/v1/chat/completions/"+deploy.ID, "openai")
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if ep.DeploymentID != deploy.ID || ep.Path == "" || ep.Protocol != "openai" {
		t.Fatalf("endpoint = %+v", ep)
	}

	// Clean up in FK-safe order: deleting the tenant cascades to deployments →
	// endpoints, then the model/template parents are free to delete (deployments
	// reference their versions with NO ACTION, so parents must go second).
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM models WHERE id = $1`, model.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM serving_templates WHERE id = $1`, tpl.ID)
	})
}

// TestResolveDeploymentIntegration resolves model name → READY deployment with
// tenant scoping, against live Postgres.
func TestResolveDeploymentIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(d.Pool())

	tenant, err := s.CreateTenant(ctx, "rd-tenant-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	model, err := s.CreateModel(ctx, Model{Name: "rd-model-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := s.CreateModelVersion(ctx, ModelVersion{ModelID: model.ID, Version: "v1", ArtifactURI: "local://m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := s.CreateTemplate(ctx, ServingTemplate{Name: "rd-template-" + uuid.NewString()[:8], Runtime: "transformers"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := s.CreateTemplateVersion(ctx, TemplateVersion{TemplateID: tpl.ID, Version: "v1", Image: "img:latest"})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}
	deploy, err := s.CreateDeployment(ctx, Deployment{
		TenantID: tenant.ID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "svc", Region: "us-east-1", DesiredReplicas: 1,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// PENDING deployment must NOT resolve.
	if _, err := s.ResolveDeployment(ctx, tenant.ID, model.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve PENDING = %v, want ErrNotFound", err)
	}

	// Drive to READY.
	for _, to := range []string{DeploymentProvisioning, DeploymentStarting, DeploymentReady} {
		if _, err := s.TransitionDeployment(ctx, deploy.ID, to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}

	got, err := s.ResolveDeployment(ctx, tenant.ID, model.Name)
	if err != nil {
		t.Fatalf("resolve READY: %v", err)
	}
	if got.ID != deploy.ID {
		t.Fatalf("resolved %q, want %q", got.ID, deploy.ID)
	}

	// Wrong tenant → ErrNotFound (tenant isolation).
	if _, err := s.ResolveDeployment(ctx, uuid.NewString(), model.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve wrong tenant = %v, want ErrNotFound", err)
	}

	// Unknown model name → ErrNotFound.
	if _, err := s.ResolveDeployment(ctx, tenant.ID, "no-such-model"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve unknown model = %v, want ErrNotFound", err)
	}

	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM models WHERE id = $1`, model.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM serving_templates WHERE id = $1`, tpl.ID)
	})
}
