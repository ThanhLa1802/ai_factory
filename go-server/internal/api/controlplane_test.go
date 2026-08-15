package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/runtime"
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

	h := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())
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

// TestCreateDeploymentPublishesEvent asserts POST /deployments returns 202 and
// publishes deployment_created on the bus.
func TestCreateDeploymentPublishesEvent(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t) // helper below
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "pub-ev-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(ctx, events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	h := NewControlPlaneHandler(cp, authSvc, secret, bus)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	// deployments has FKs to model_versions / serving_template_versions, so
	// create real catalog rows first.
	model, err := cp.CreateModel(ctx, controlplane.Model{Name: "qwen-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := cp.CreateModelVersion(ctx, controlplane.ModelVersion{ModelID: model.ID, Version: "1.0", ArtifactURI: "file:///m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := cp.CreateTemplate(ctx, controlplane.ServingTemplate{Name: "tpl-" + uuid.NewString()[:8], Runtime: "python"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := cp.CreateTemplateVersion(ctx, controlplane.TemplateVersion{TemplateID: tpl.ID, Version: "1.0", Image: "img"})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model_version_id": mv.ID, "template_version_id": tv.ID,
		"name": "svc", "region": "us-east-1", "desired_replicas": 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create deployment code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if len(published) != 1 || published[0].Type != events.TypeDeploymentCreated {
		t.Fatalf("published = %+v, want exactly one deployment_created", published)
	}
}

// TestCreateDeploymentIdempotencyKey asserts two POST /deployments with the same
// Idempotency-Key return the same deployment (no duplicate).
func TestCreateDeploymentIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "idem-api-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RolePlatformAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := events.NewMemoryEventBus()
	h := NewControlPlaneHandler(cp, authSvc, secret, bus)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	model, err := cp.CreateModel(ctx, controlplane.Model{Name: "qwen-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := cp.CreateModelVersion(ctx, controlplane.ModelVersion{ModelID: model.ID, Version: "1.0", ArtifactURI: "file:///m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := cp.CreateTemplate(ctx, controlplane.ServingTemplate{Name: "tpl-" + uuid.NewString()[:8], Runtime: "python"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := cp.CreateTemplateVersion(ctx, controlplane.TemplateVersion{TemplateID: tpl.ID, Version: "1.0", Image: "img"})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model_version_id": mv.ID, "template_version_id": tv.ID,
		"name": "svc", "region": "us-east-1", "desired_replicas": 1,
	})

	create := func(wantStatus int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "deploy-idem-1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("create deployment code = %d (want %d), body = %s", rec.Code, wantStatus, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	first := create(http.StatusAccepted)
	second := create(http.StatusOK) // replay, not a new create
	if first["id"] != second["id"] {
		t.Fatalf("idempotency violated: first id=%v second id=%v", first["id"], second["id"])
	}
}

func dbConnOrSkip(t *testing.T) *db.DB {
	t.Helper()
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
	return d
}

func loginHelper(t *testing.T, mux *http.ServeMux, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
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
	return resp.AccessToken
}

// TestAsyncDeployE2E drives the full M2 path: create deployment via HTTP -> the
// memory bus synchronously invokes the worker -> deployment reaches READY.
func TestAsyncDeployE2E(t *testing.T) {
	d := dbConnOrSkip(t)
	ctx := context.Background()
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	suffix := uuid.NewString()[:8]
	tenant, err := cp.CreateTenant(ctx, "async-"+suffix)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	hash, _ := auth.HashPassword("admin-pass")
	// NOTE: catalog writes (models/templates) are platform-admin only in the M1
	// RBAC, so this E2E boots a PLATFORM_ADMIN (deviation from brief, which used
	// TENANT_ADMIN — that role cannot POST /api/v1/models|/templates → 403).
	user, err := cp.CreateUser(ctx, "admin-"+suffix, "admin-"+suffix+"@io", hash, auth.RolePlatformAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := events.NewMemoryEventBus()
	worker := runtime.NewWorker(cp, runtime.NewWorkerAdapter("localhost:1"), runtime.NewMockComputeProvider(), bus, bus,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := worker.Run(ctx); err != nil { // Subscribe is non-blocking
		t.Fatalf("worker run: %v", err)
	}

	h := NewControlPlaneHandler(cp, authSvc, secret, bus)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	post := func(path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("POST %s code = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return out
	}
	get := func(path string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s code = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return out
	}

	model := post("/api/v1/models", map[string]any{"name": "qwen-" + suffix, "task": "text-generation", "framework": "transformers"}, http.StatusCreated)
	mv := post("/api/v1/models/"+model["id"].(string)+"/versions",
		map[string]any{"version": "1.0", "artifact_uri": "file:///m"}, http.StatusCreated)
	tpl := post("/api/v1/templates", map[string]any{"name": "tpl-" + suffix, "runtime": "python"}, http.StatusCreated)
	tv := post("/api/v1/templates/"+tpl["id"].(string)+"/versions",
		map[string]any{"version": "1.0", "image": "ai-factory:latest"}, http.StatusCreated)

	dep := post("/api/v1/deployments", map[string]any{
		"model_version_id": mv["id"].(string),
		"template_version_id": tv["id"].(string),
		"name": "svc-" + suffix, "region": "us-east-1", "desired_replicas": 1,
	}, http.StatusAccepted)
	depID := dep["id"].(string)

	// Memory bus dispatch is synchronous: the worker finished before 202 returned.
	got := get("/api/v1/deployments/"+depID, http.StatusOK)
	if got["status"] != "READY" {
		t.Fatalf("deployment status = %v, want READY", got["status"])
	}
	if got["workload_ref"] == "" {
		t.Fatal("deployment workload_ref empty, want mock ref")
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

	h := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())
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
	h := NewHandler(sess, loop, t.TempDir(), authSvc, secret, cp, nil, 60, 4)

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
