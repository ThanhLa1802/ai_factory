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

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/session"
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

// TestAPIKeyLifecycleE2E: tạo tenant/user/login (pattern giống TestLoginE2E), rồi
// tạo key → list → delete → list rỗng.
func TestAPIKeyLifecycleE2E(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
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
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	suffix := uuid.NewString()[:8]
	tenant, err := cp.CreateTenant(ctx, "keys-e2e-"+suffix)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	hash, _ := auth.HashPassword("admin-pass")
	user, err := cp.CreateUser(ctx, "kuser-"+suffix, "k@e2e-"+suffix+".io", hash, auth.RoleTenantAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	h := NewControlPlaneHandler(cp, authSvc, secret)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// login
	lb, _ := json.Marshal(map[string]string{"username": "kuser-" + suffix, "password": "admin-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(lb))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	authH := func() string { return "Bearer " + login.AccessToken }

	// create key
	cb, _ := json.Marshal(map[string]string{"name": "e2e-key"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewReader(cb))
	req.Header.Set("Authorization", authH())
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || created.Key == "" {
		t.Fatalf("create key resp = %s, want id + raw key", rec.Body.String())
	}

	// list contains it
	req = httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", authH())
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(created.ID)) {
		t.Fatalf("list body = %s, want contain key id %s", rec.Body.String(), created.ID)
	}

	// delete
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/api-keys/"+created.ID, nil)
	req.Header.Set("Authorization", authH())
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// list empty
	req = httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", authH())
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte(created.ID)) {
		t.Fatalf("after delete: code = %d, body = %s, want 200 without key", rec.Code, rec.Body.String())
	}
}

// TestInferenceAuthRequiredE2E: /v1/chat/completions không auth phải 401 (không cần worker,
// middleware chặn trước khi vào handler). Dựng api.Handler với authSvc + secret thật.
func TestInferenceAuthRequiredE2E(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
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
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	// worker addr chỉ dùng khi gọi thật; grpc.NewClient là lazy nên không cần worker chạy
	ic, err := inference.NewClient("localhost:59999")
	if err != nil {
		t.Fatalf("inference.NewClient: %v", err)
	}
	t.Cleanup(func() { ic.Close() })
	bs := inference.NewBatchScheduler(ic)
	te := agent.NewLocalToolExecutor(t.TempDir())
	loop := agent.NewLoop(bs, te)
	sess := session.NewManager()
	h := NewHandler(sess, loop, t.TempDir(), authSvc, secret)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth code = %d, body = %s, want 401", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"UNAUTHORIZED"`)) {
		t.Errorf("body = %s, want UNAUTHORIZED json", rec.Body.String())
	}
}
