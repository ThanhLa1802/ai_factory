package controlplane

import (
	"context"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/db"
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
	tenant, err := s.CreateTenant(ctx, "wl-test")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	deploy, err := s.CreateDeployment(ctx, Deployment{
		TenantID: tenant.ID, ModelVersionID: "mv-x", TemplateVersionID: "tv-x",
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
}
