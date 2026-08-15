package controlplane

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/db"
	"github.com/google/uuid"
)

// TestIdempotencyKeyIntegration runs against a live Postgres (set
// AI_FACTORY_DATABASE_URL), mirroring the other controlplane integration tests.
func TestIdempotencyKeyIntegration(t *testing.T) {
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

	tenant, err := s.CreateTenant(ctx, "idem-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	// Unknown key → ErrNotFound.
	if _, err := s.ResolveIdempotencyKey(ctx, tenant.ID, "k1", "deployment"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve unknown = %v, want ErrNotFound", err)
	}

	// Save + resolve round trip.
	depID := uuid.NewString()
	if err := s.SaveIdempotencyKey(ctx, tenant.ID, "k1", "deployment", depID); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.ResolveIdempotencyKey(ctx, tenant.ID, "k1", "deployment")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != depID {
		t.Fatalf("resolved = %q, want %q", got, depID)
	}

	// Keys are tenant-scoped: another tenant sees ErrNotFound.
	other, err := s.CreateTenant(ctx, "idem-other-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, other.ID) })
	if _, err := s.ResolveIdempotencyKey(ctx, other.ID, "k1", "deployment"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve cross-tenant = %v, want ErrNotFound", err)
	}
}
