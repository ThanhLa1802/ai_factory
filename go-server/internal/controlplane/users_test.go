package controlplane

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/ai-factory/go-server/internal/db"
)

// SQL generation is validated through the integration test below (gated by env)
// and the E2E in Task 9. TestNewService is the fail-first gate for the stub.
func TestNewService(t *testing.T) {
	svc := NewService(nil)
	if svc == nil {
		t.Fatal("NewService(nil) returned nil")
	}
}

func TestUsesUUID(t *testing.T) {
	if id := uuid.NewString(); len(id) != 36 {
		t.Errorf("uuid length = %d, want 36", len(id))
	}
}

// TestTenantUserAPIKeyIntegration runs against a live Postgres (set AI_FACTORY_DATABASE_URL).
func TestTenantUserAPIKeyIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Register the pool close FIRST: t.Cleanup runs LIFO, so the delete
	// cleanups below execute before the pool is closed.
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewService(d.Pool())

	tenant, err := svc.CreateTenant(ctx, "it-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateTenant error = %v", err)
	}
	username := "it-user-" + uuid.NewString()[:8]
	email := "it-" + uuid.NewString()[:8] + "@example.com"
	user, err := svc.CreateUser(ctx, username, email, "hash", "TENANT_ADMIN", tenant.ID)
	if err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	if user.TenantID != tenant.ID {
		t.Errorf("user.TenantID = %q, want %q", user.TenantID, tenant.ID)
	}
	got, hash, err := svc.GetUserByUsername(ctx, username)
	if err != nil || got.ID != user.ID || hash != "hash" {
		t.Errorf("GetUserByUsername = (%+v, %q, %v)", got, hash, err)
	}
	keyHash := "hash-" + uuid.NewString()[:8]
	key, err := svc.CreateAPIKey(ctx, tenant.ID, "it-key", keyHash, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey error = %v", err)
	}
	found, err := svc.GetAPIKeyByHash(ctx, keyHash)
	if err != nil || found.ID != key.ID {
		t.Errorf("GetAPIKeyByHash = (%+v, %v)", found, err)
	}
	// Clean up the created rows by exact id. Registered parent-first because
	// t.Cleanup runs LIFO: api_key → user (cascades memberships) → tenant.
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
}

// TestListDeleteAPIKeyIntegration chạy với Postgres thật (set AI_FACTORY_DATABASE_URL).
func TestListDeleteAPIKeyIntegration(t *testing.T) {
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
	svc := NewService(d.Pool())

	tenant, err := svc.CreateTenant(ctx, "keys-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	// keyHash chỉ cần là string bất kỳ — List/Delete không validate hash (tránh import auth → cycle)
	k, err := svc.CreateAPIKey(ctx, tenant.ID, "test-key", "testhash-"+uuid.NewString(), nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	keys, err := svc.ListAPIKeys(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != k.ID {
		t.Fatalf("ListAPIKeys = %+v, want 1 key %s", keys, k.ID)
	}

	// scoping: key thuộc tenant A không xoá được bởi tenant B — test TRƯỚC khi xoá thật
	other, err := svc.CreateTenant(ctx, "keys-other-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateTenant other: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, other.ID) })
	if err := svc.DeleteAPIKey(ctx, k.ID, other.ID); err != ErrNotFound {
		t.Errorf("delete with wrong tenant = %v, want ErrNotFound", err)
	}

	// xoá thật (tenant đúng)
	if err := svc.DeleteAPIKey(ctx, k.ID, tenant.ID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	keys, _ = svc.ListAPIKeys(ctx, tenant.ID)
	if len(keys) != 0 {
		t.Errorf("after delete list = %+v, want empty", keys)
	}
	// xoá lần 2 → ErrNotFound
	if err := svc.DeleteAPIKey(ctx, k.ID, tenant.ID); err != ErrNotFound {
		t.Errorf("delete again = %v, want ErrNotFound", err)
	}
}
