package app

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

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	infrainf "github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/ai-factory/go-server/internal/services/iam"
	inferencesvc "github.com/ai-factory/go-server/internal/services/inference"
	"github.com/ai-factory/go-server/internal/services/serving"
	"github.com/ai-factory/go-server/internal/services/usage"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// testServices bundles the split IAM + control-plane services for E2E tests.
type testServices struct {
	iam     *iam.Service
	authSvc *iam.AuthService
	authn   *iam.Authenticator
	serving *serving.Service
	usage   *usage.Service
}

func newTestServices(t *testing.T, d *database.DB) *testServices {
	t.Helper()
	secret := []byte("0123456789abcdef")
	iamSvc := iam.NewServiceFromGorm(d.Gorm())
	authSvc := iam.NewAuthService(iamSvc, secret, time.Hour)
	return &testServices{
		iam:     iamSvc,
		authSvc: authSvc,
		authn:   iam.NewAuthenticator(secret, authSvc),
		serving: serving.NewServiceFromGorm(d.Gorm()),
		usage:   usage.NewServiceFromGorm(d.Gorm()),
	}
}

func (ts *testServices) mountIAM(mux *gin.Engine) {
	iam.NewHandler(ts.iam, ts.authSvc, ts.authn).RegisterRoutes(mux)
}

func (ts *testServices) mountServing(mux *gin.Engine, producer message.Producer) {
	serving.NewHandler(ts.serving, ts.authn, producer).RegisterRoutes(mux)
}

func (ts *testServices) mountControlPlane(mux *gin.Engine) {
	usage.NewHandler(ts.usage, ts.authn).RegisterRoutes(mux)
}

// testResolver adapts serving.Service to the inference handler's resolver.
type testResolver struct{ svc *serving.Service }

func (r testResolver) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*inferencesvc.ResolvedDeployment, error) {
	d, err := r.svc.ResolveDeployment(ctx, tenantID, modelName)
	if err != nil {
		return nil, err
	}
	return &inferencesvc.ResolvedDeployment{ID: d.ID, TenantID: d.TenantID, Region: d.Region}, nil
}

// TestLoginE2E runs the full M1 control plane path against a real Postgres.
func TestLoginE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)

	ts := newTestServices(t, d)

	// bootstrap: tenant + admin user (randomized so the DB stays clean for the
	// seed/demo flow and re-runs never collide with UNIQUE(name/username/email))
	suffix := uuid.NewString()[:8]
	tenantName := "acme-" + suffix
	username := "admin-" + suffix
	email := "admin@acme-" + suffix + ".io"

	tenant, err := ts.iam.CreateTenant(ctx, tenantName)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})
	hash, _ := iam.HashPassword("admin-pass")
	user, err := ts.iam.CreateUser(ctx, username, email, hash, iam.RoleTenantAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID)
	})

	mux := newTestEngine()
	ts.mountIAM(mux)
	loginHelper(t, mux, user.Username, "admin-pass")
}

// TestCreateDeploymentPublishesEvent asserts POST /deployments returns 202 and
// publishes deployment_created on the bus.
func TestCreateDeploymentPublishesEvent(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	ts := newTestServices(t, d)

	tenant, _ := ts.iam.CreateTenant(ctx, "pub-ev-"+uuid.NewString()[:8])
	hash, _ := iam.HashPassword("admin-pass")
	user, _ := ts.iam.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, iam.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := message.NewMemoryEventBus()
	var published []message.Event
	if err := bus.Subscribe(ctx, message.TopicDeploymentEvents,
		func(ctx context.Context, ev message.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	mux := newTestEngine()
	ts.mountIAM(mux)
	ts.mountServing(mux, bus)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	// deployments has FKs to model_versions / serving_template_versions, so
	// create real catalog rows first.
	model, err := ts.serving.CreateModel(ctx, serving.Model{Name: "qwen-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := ts.serving.CreateModelVersion(ctx, serving.ModelVersion{ModelID: model.ID, Version: "1.0", ArtifactURI: "file:///m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := ts.serving.CreateTemplate(ctx, serving.ServingTemplate{Name: "tpl-" + uuid.NewString()[:8], Runtime: "python"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := ts.serving.CreateTemplateVersion(ctx, serving.TemplateVersion{TemplateID: tpl.ID, Version: "1.0", Image: "img"})
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

	if len(published) != 1 || published[0].Type != message.TypeDeploymentCreated {
		t.Fatalf("published = %+v, want exactly one deployment_created", published)
	}
}

// TestCreateDeploymentIdempotencyKey asserts two POST /deployments with the same
// Idempotency-Key return the same deployment (no duplicate).
func TestCreateDeploymentIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	ts := newTestServices(t, d)

	tenant, _ := ts.iam.CreateTenant(ctx, "idem-api-"+uuid.NewString()[:8])
	hash, _ := iam.HashPassword("admin-pass")
	user, _ := ts.iam.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, iam.RolePlatformAdmin, tenant.ID)
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := message.NewMemoryEventBus()
	mux := newTestEngine()
	ts.mountIAM(mux)
	ts.mountServing(mux, bus)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	model, err := ts.serving.CreateModel(ctx, serving.Model{Name: "qwen-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := ts.serving.CreateModelVersion(ctx, serving.ModelVersion{ModelID: model.ID, Version: "1.0", ArtifactURI: "file:///m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := ts.serving.CreateTemplate(ctx, serving.ServingTemplate{Name: "tpl-" + uuid.NewString()[:8], Runtime: "python"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := ts.serving.CreateTemplateVersion(ctx, serving.TemplateVersion{TemplateID: tpl.ID, Version: "1.0", Image: "img"})
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

func dbConnOrSkip(t *testing.T) *database.DB {
	t.Helper()
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
	}
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func loginHelper(t *testing.T, mux *gin.Engine, user, pass string) string {
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
	ts := newTestServices(t, d)

	suffix := uuid.NewString()[:8]
	tenant, err := ts.iam.CreateTenant(ctx, "async-"+suffix)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	hash, _ := iam.HashPassword("admin-pass")
	// NOTE: catalog writes (models/templates) are platform-admin only in the M1
	// RBAC, so this E2E boots a PLATFORM_ADMIN (deviation from brief, which used
	// TENANT_ADMIN — that role cannot POST /api/v1/models|/templates → 403).
	user, err := ts.iam.CreateUser(ctx, "admin-"+suffix, "admin-"+suffix+"@io", hash, iam.RolePlatformAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := message.NewMemoryEventBus()
	worker := serving.NewWorker(ts.serving, serving.NewWorkerAdapter("localhost:1"), serving.NewMockComputeProvider(), bus, bus,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := worker.Run(ctx); err != nil { // Subscribe is non-blocking
		t.Fatalf("worker run: %v", err)
	}

	mux := newTestEngine()
	ts.mountIAM(mux)
	ts.mountServing(mux, bus)
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
		"model_version_id":    mv["id"].(string),
		"template_version_id": tv["id"].(string),
		"name":                "svc-" + suffix, "region": "us-east-1", "desired_replicas": 1,
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
	ctx := context.Background()
	d := dbConnOrSkip(t)
	ts := newTestServices(t, d)

	suffix := uuid.NewString()[:8]
	tenant, err := ts.iam.CreateTenant(ctx, "keys-e2e-"+suffix)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	hash, _ := iam.HashPassword("admin-pass")
	user, err := ts.iam.CreateUser(ctx, "kuser-"+suffix, "k@e2e-"+suffix+".io", hash, iam.RoleTenantAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID) })

	mux := newTestEngine()
	ts.mountIAM(mux)

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
// middleware chặn trước khi vào handler). Dựng inference.Handler với authenticator thật.
func TestInferenceAuthRequiredE2E(t *testing.T) {
	d := dbConnOrSkip(t)
	ts := newTestServices(t, d)

	// worker addr chỉ dùng khi gọi thật; grpc.NewClient là lazy nên không cần worker chạy
	ic, err := infrainf.NewClient("localhost:59999")
	if err != nil {
		t.Fatalf("inference.NewClient: %v", err)
	}
	t.Cleanup(func() { ic.Close() })
	bs := infrainf.NewBatchScheduler(ic)
	te := inferencesvc.NewLocalToolExecutor(t.TempDir())
	loop := inferencesvc.NewLoop(bs, te)
	sess := inferencesvc.NewManager()
	h := inferencesvc.NewHandler(sess, loop, t.TempDir(), ts.authn, testResolver{ts.serving}, ts.usage, nil, 60, 4)

	mux := newTestEngine()
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
