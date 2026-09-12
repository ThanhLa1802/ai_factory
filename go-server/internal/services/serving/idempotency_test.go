package serving

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
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
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewServiceFromGorm(d.Gorm())

	tenantID := uuid.NewString()
	if err := d.Gorm().Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, tenantID, "idem-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenantID) })

	// Unknown key → ErrNotFound.
	if _, err := s.ResolveIdempotencyKey(ctx, tenantID, "k1", "deployment"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve unknown = %v, want ErrNotFound", err)
	}

	// Save + resolve round trip.
	depID := uuid.NewString()
	if err := s.SaveIdempotencyKey(ctx, tenantID, "k1", "deployment", depID); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.ResolveIdempotencyKey(ctx, tenantID, "k1", "deployment")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != depID {
		t.Fatalf("resolved = %q, want %q", got, depID)
	}

	// Keys are tenant-scoped: another tenant sees ErrNotFound.
	otherID := uuid.NewString()
	if err := d.Gorm().Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, otherID, "idem-other-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, otherID) })
	if _, err := s.ResolveIdempotencyKey(ctx, otherID, "k1", "deployment"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve cross-tenant = %v, want ErrNotFound", err)
	}
}
