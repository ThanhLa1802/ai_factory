package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
	"github.com/google/uuid"
)

// TestLoginE2E runs the full M1 control plane path against a real Postgres.
func TestLoginE2E(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
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

	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	// bootstrap: tenant + admin user (randomized so the DB stays clean for the
	// seed/demo flow and re-runs never collide with UNIQUE(name/username/email))
	suffix := uuid.NewString()[:8]
	tenantName := "acme-" + suffix
	username := "admin-" + suffix
	email := "admin@acme-" + suffix + ".io"

	tenant, err := cp.CreateTenant(ctx, tenantName)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})
	hash, _ := auth.HashPassword("admin-pass")
	user, err := cp.CreateUser(ctx, username, email, hash, auth.RoleTenantAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	h := NewControlPlaneHandler(cp, authSvc, secret)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// login
	loginBody, _ := json.Marshal(map[string]string{"username": username, "password": "admin-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal login: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("empty access_token")
	}
}
