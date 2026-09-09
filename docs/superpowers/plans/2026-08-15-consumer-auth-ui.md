# Consumer Slice — Auth trên Inference + UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gắn auth (JWT hoặc API key) bắt buộc lên `/v1/chat/completions` + `/v1/messages`, thêm list/revoke API key ở control plane, và dựng UI 3 trang (login → chat + quản lý API key) để người dùng đăng nhập dùng model hoặc lấy key gọi model.

**Architecture:** Middleware `auth.InferenceAuth` bọc 2 route inference, chấp nhận Bearer JWT (parse qua `ParseToken`, claims vào context) hoặc Bearer API key (qua `svc.AuthenticateAPIKey`, key vào context). Control plane thêm `GET`/`DELETE /api/v1/api-keys` (gated `key.manage`). Go serve 3 trang HTML tĩnh; `/chat` gọi `/v1/chat/completions` kèm Bearer JWT.

**Tech Stack:** Go (net/http, chi tiết trong `go-server`), vanilla HTML/JS không build, Postgres (control plane, test gated `AI_FACTORY_DATABASE_URL`).

## Global Constraints

- Module path: `github.com/ai-factory/go-server`. Tất cả code trong worktree `G:\STUDY\AI\ai_factory\.claude\worktrees\feature+m1`.
- **Không log API key / token / password / raw prompt** (rule bắt buộc). Middleware tuyệt đối không log giá trị token.
- **Không đổi signature của `auth.RequireAuth`, `auth.RequirePermission`, `ctxKey`, `ClaimsFromContext`** — các route control plane hiện có phụ thuộc.
- Auth error dùng `writeAuthError(w, status, code, msg)` → `{"error":{"code":...,"message":...}}` (đã có trong `auth/middleware.go`).
- Test DB-gating: nếu `os.Getenv("AI_FACTORY_DATABASE_URL") == ""` → `t.Skip("... not set; skipping ...")` (convention hiện có, xem `TestLoginE2E`).
- `NewHandler` thêm 2 param `authSvc *auth.Service, secret []byte` — **cập nhật mọi call site** (main.go + test).
- `controlplane` internal test (`package controlplane`) **không import `auth`** (cycle: auth→controlplane). Dùng hash string thường, không gọi `auth.GenerateAPIKey`.
- UI: vanilla HTML/JS, không build step, không thêm dependency Go hay npm.
- Sử dụng sentinel error với `errors.Is` khi so `ErrNotFound`.

---

### Task 1: Auth middleware `InferenceAuth` + `APIKeyFromContext`

**Files:**
- Modify: `go-server/internal/auth/middleware.go` (thêm `apiKeyCtxKey`, `APIKeyFromContext`, `InferenceAuth`; thêm import `"errors"`, `"github.com/ai-factory/go-server/internal/controlplane"`)
- Modify: `go-server/internal/auth/middleware_test.go` (thêm tests; `fakeStore` đã có sẵn trong `service_test.go` cùng package `auth`)

**Interfaces:**
- Consumes: `auth.ParseToken(secret, tok) (*Claims, error)` (jwt.go), `(*Service).AuthenticateAPIKey(ctx, raw) (*controlplane.APIKey, error)` (service.go), `ctxKey`, `writeAuthError`, `ClaimsFromContext`, `ErrInvalidAPIKey`, `ErrKeyInactive` (service.go).
- Produces: `func InferenceAuth(secret []byte, svc *Service) func(http.Handler) http.Handler`, `func APIKeyFromContext(ctx context.Context) (*controlplane.APIKey, bool)` — Task 4 dùng `InferenceAuth` để bọc route.

- [ ] **Step 1: Viết test fail** — thêm vào `middleware_test.go`:

```go
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

func TestInferenceAuthJWT(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, err := IssueToken(secret, "u1", "t1", RoleTenantAdmin, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	svc := NewService(&fakeStore{}, secret, time.Hour)
	var gotTenant string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			t.Error("ClaimsFromContext: not ok")
		}
		gotTenant = claims.TenantID
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if gotTenant != "t1" {
		t.Errorf("tenant = %q, want t1", gotTenant)
	}
}

func TestInferenceAuthAPIKey(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t9", Status: "ACTIVE"}}, secret, time.Hour)
	// raw key bất kỳ: fakeStore trả key cố định, không cần hash khớp
	var gotTenant string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Error("APIKeyFromContext: not ok")
		}
		gotTenant = key.TenantID
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-whatever")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if gotTenant != "t9" {
		t.Errorf("tenant = %q, want t9", gotTenant)
	}
}

func TestInferenceAuthMissingHeader(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run without auth")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil) // no Authorization
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("body = %s, want UNAUTHORIZED error json", rec.Body.String())
	}
}

func TestInferenceAuthInvalidToken(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run on invalid token")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer not-a-token-not-a-key")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestInferenceAuthInactiveKey(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t9", Status: "REVOKED"}}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run on inactive key")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-inactive")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("body = %s, want FORBIDDEN error json", rec.Body.String())
	}
}
```

Lưu ý: `strings.Contains` cần thêm `"strings"` vào import của `middleware_test.go`.

- [ ] **Step 2: Chạy test → fail**

Run: `cd go-server && go test ./internal/auth/ -run TestInferenceAuth -v`
Expected: FAIL với `undefined: InferenceAuth` (chưa có hàm).

- [ ] **Step 3: Cài implementation** — thêm vào cuối `middleware.go`:

```go
// apiKeyCtxKey marks an API key stored in the request context.
type apiKeyCtxKey struct{}

// APIKeyFromContext extracts the API key authenticated by InferenceAuth.
func APIKeyFromContext(ctx context.Context) (*controlplane.APIKey, bool) {
	k, ok := ctx.Value(apiKeyCtxKey{}).(*controlplane.APIKey)
	return k, ok
}

// InferenceAuth gates inference routes behind a Bearer JWT or a Bearer API key.
// A JWT puts Claims in the context under ctxKey (so ClaimsFromContext works); an
// API key goes under apiKeyCtxKey (read via APIKeyFromContext). Invalid → 401;
// inactive/expired key → 403. Never logs the raw token.
func InferenceAuth(secret []byte, svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
				return
			}
			tok := strings.TrimPrefix(h, prefix)
			if claims, err := ParseToken(secret, tok); err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
				return
			}
			key, err := svc.AuthenticateAPIKey(r.Context(), tok)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), apiKeyCtxKey{}, key)))
				return
			}
			if errors.Is(err, ErrKeyInactive) {
				writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "API key inactive or expired")
				return
			}
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		})
	}
}
```

Thêm import: `"errors"` và `"github.com/ai-factory/go-server/internal/controlplane"` vào `middleware.go`.

- [ ] **Step 4: Chạy test → pass**

Run: `cd go-server && go test ./internal/auth/ -run TestInferenceAuth -v`
Expected: 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add go-server/internal/auth/middleware.go go-server/internal/auth/middleware_test.go
git commit -m "feat(auth): InferenceAuth — Bearer JWT hoặc API key cho inference routes"
```

---

### Task 2: controlplane `ListAPIKeys` + `DeleteAPIKey` + `ErrNotFound`

**Files:**
- Modify: `go-server/internal/controlplane/users.go` (thêm `ErrNotFound`, 2 method; thêm import `"errors"`)
- Modify: `go-server/internal/controlplane/users_test.go` (thêm integration test gated)

**Interfaces:**
- Consumes: pattern SQL của `CreateAPIKey`/`GetAPIKeyByHash` (users.go), struct `APIKey` (types.go:22), `db *pgxpool.Pool`.
- Produces: `func (s *Service) ListAPIKeys(ctx, tenantID) ([]APIKey, error)`, `func (s *Service) DeleteAPIKey(ctx, id, tenantID) error`, `var ErrNotFound = errors.New("not found")` — Task 3 dùng trong handler.

- [ ] **Step 1: Viết test fail** — thêm vào cuối `users_test.go`:

```go
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
```

- [ ] **Step 2: Chạy test → fail**

Run: `cd go-server && go test ./internal/controlplane/ -run TestListDeleteAPIKeyIntegration -v`
Expected: FAIL — compile error `undefined: ListAPIKeys` / `undefined: ErrNotFound`.

- [ ] **Step 3: Cài implementation** — thêm vào `users.go` (sau `GetAPIKeyByHash`):

```go
var ErrNotFound = errors.New("not found")

// ListAPIKeys trả các API key của một tenant, mới nhất trước.
func (s *Service) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, name, status, expires_at, created_at
		 FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey xoá key theo id + tenant (scoping an toàn). ErrNotFound nếu không khớp.
func (s *Service) DeleteAPIKey(ctx context.Context, id, tenantID string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
```

Thêm import `"errors"` vào `users.go`.

- [ ] **Step 4: Chạy test → pass**

Run: `cd go-server && go test ./internal/controlplane/ -run TestListDeleteAPIKeyIntegration -v`
Expected: PASS (cần Postgres chạy + `AI_FACTORY_DATABASE_URL` set; nếu không có DB thì test skip — xác nhận skip bằng output).

- [ ] **Step 5: Commit**

```bash
git add go-server/internal/controlplane/users.go go-server/internal/controlplane/users_test.go
git commit -m "feat(controlplane): ListAPIKeys + DeleteAPIKey + ErrNotFound"
```

---

### Task 3: Control plane API handlers — GET/DELETE `/api/v1/api-keys`

**Files:**
- Modify: `go-server/internal/api/controlplane.go` (thêm 2 route trong `RegisterRoutes` cạnh dòng 28; thêm 2 handler; thêm import `"errors"`)
- Modify: `go-server/internal/api/controlplane_test.go` (thêm `TestAPIKeyLifecycleE2E` gated)

**Interfaces:**
- Consumes: `auth.RequirePermission(h.secret, auth.ActionKeyManage)`, `auth.ClaimsFromContext`, `h.cp.ListAPIKeys`/`h.cp.DeleteAPIKey` (Task 2), `controlplane.ErrNotFound`, `writeAPIError`/`writeJSON` (đã có).
- Produces: `handleListAPIKeys`, `handleDeleteAPIKey` + 2 route. Task 5 (UI keys.html) gọi 2 endpoint này.

- [ ] **Step 1: Viết test fail** — thêm vào cuối `controlplane_test.go`:

```go
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
		ID string `json:"id"`
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
```

- [ ] **Step 2: Chạy test → fail**

Run: `cd go-server && go test ./internal/api/ -run TestAPIKeyLifecycleE2E -v`
Expected: FAIL — GET/DELETE trả 404 (route chưa tồn tại) hoặc compile lỗi.

- [ ] **Step 3: Cài implementation** — trong `controlplane.go`:

Route (thêm ngay sau dòng `POST /api/v1/api-keys` trong `RegisterRoutes`):

```go
	mux.Handle("GET /api/v1/api-keys", auth.RequirePermission(h.secret, auth.ActionKeyManage)(http.HandlerFunc(h.handleListAPIKeys)))
	mux.Handle("DELETE /api/v1/api-keys/", auth.RequirePermission(h.secret, auth.ActionKeyManage)(http.HandlerFunc(h.handleDeleteAPIKey)))
```

Handler (thêm sau `handleCreateAPIKey`):

```go
func (h *ControlPlaneHandler) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	keys, err := h.cp.ListAPIKeys(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *ControlPlaneHandler) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/api-keys/")
	if err := h.cp.DeleteAPIKey(r.Context(), id, claims.TenantID); err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "api key not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Thêm import `"errors"` vào `controlplane.go` (nếu chưa có).

- [ ] **Step 4: Chạy test → pass**

Run: `cd go-server && go test ./internal/api/ -run TestAPIKeyLifecycleE2E -v`
Expected: PASS (cần Postgres; skip nếu không có DB).

- [ ] **Step 5: Commit**

```bash
git add go-server/internal/api/controlplane.go go-server/internal/api/controlplane_test.go
git commit -m "feat(api): GET/DELETE /api/v1/api-keys + E2E lifecycle"
```

---

### Task 4: Wire `InferenceAuth` vào Handler + main + routing UI

**Files:**
- Modify: `go-server/internal/api/handler.go` (struct Handler thêm `authSvc`/`secret`; `NewHandler` thêm 2 param; bọc 2 route inference; đổi `handleUI` route theo path; thêm import `"github.com/ai-factory/go-server/internal/auth"`)
- Modify: `go-server/cmd/server/main.go` (dòng 103 truyền thêm `authSvc, []byte(cfg.JWTSecret)`)
- Modify: `go-server/internal/api/controlplane_test.go` (thêm `TestInferenceAuthRequiredE2E` gated)
- Modify: `go-server/internal/api/handler_test.go` — **tạo mới** nếu chưa có (thêm `TestHandleUIRouting` không gated)

**Interfaces:**
- Consumes: `auth.InferenceAuth` (Task 1), `auth.Service`, `auth.ClaimsFromContext` (không bắt buộc trong slice), `session.Manager`, `agent.Loop`.
- Produces: `NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string, authSvc *auth.Service, secret []byte) *Handler`. UI routing: `/`→index.html, `/chat`→chat.html, `/keys`→keys.html.

- [ ] **Step 1: Viết test fail** — 2 test mới:

Trong `handler_test.go` (file mới, package `api`):

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestHandleUIRouting: handleUI route theo path chính xác, không gated (tạo file tạm).
func TestHandleUIRouting(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "chat.html", "keys.html"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	h := &Handler{uiDir: dir}
	for path, want := range map[string]string{"/": "index.html", "/chat": "chat.html", "/keys": "keys.html"} {
		rec := httptest.NewRecorder()
		h.handleUI(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → code %d body %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	rec := httptest.NewRecorder()
	h.handleUI(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/nope → code %d, want 404", rec.Code)
	}
}
```

Trong `controlplane_test.go` (gated — nối route inference, assert no-auth → 401). **Cần thêm import**: `"github.com/ai-factory/go-server/internal/agent"`, `"github.com/ai-factory/go-server/internal/inference"`, `"github.com/ai-factory/go-server/internal/session"` (các import còn lại đã có sẵn trong file).

```go
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
```

- [ ] **Step 2: Chạy test → fail**

Run: `cd go-server && go test ./internal/api/ -run 'TestHandleUIRouting|TestInferenceAuthRequiredE2E' -v`
Expected: FAIL — `NewHandler` chưa nhận 2 param mới (compile error) hoặc routing cũ serve chat.html cho `/`.

- [ ] **Step 3: Cài implementation** — trong `handler.go`:

Struct + constructor:

```go
type Handler struct {
	sessionMgr *session.Manager
	loop       *agent.Loop
	uiDir      string // thư mục chứa UI tĩnh (index.html, chat.html, keys.html)
	authSvc    *auth.Service
	secret     []byte
}

func NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string, authSvc *auth.Service, secret []byte) *Handler {
	return &Handler{
		sessionMgr: sessionMgr,
		loop:       loop,
		uiDir:      uiDir,
		authSvc:    authSvc,
		secret:     secret,
	}
}
```

`RegisterRoutes` — bọc 2 route inference (thay `mux.HandleFunc` bằng `mux.Handle` + middleware):

```go
	mux.Handle("/v1/chat/completions", auth.InferenceAuth(h.secret, h.authSvc)(http.HandlerFunc(h.handleOpenAIChatCompletions)))
	mux.Handle("/v1/messages", auth.InferenceAuth(h.secret, h.authSvc)(http.HandlerFunc(h.handleAnthropicMessages)))
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/v1/sessions/", h.handleSessions)
```

`handleUI` — route theo path:

```go
func (h *Handler) handleUI(w http.ResponseWriter, r *http.Request) {
	var file string
	switch r.URL.Path {
	case "/":
		file = "index.html"
	case "/chat":
		file = "chat.html"
	case "/keys":
		file = "keys.html"
	default:
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.uiDir, file))
}
```

Trong `main.go` (dòng 103):

```go
	handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret))
```

- [ ] **Step 4: Chạy test → pass**

Run: `cd go-server && go vet ./... && go test ./internal/api/ -run 'TestHandleUIRouting|TestInferenceAuthRequiredE2E' -v`
Expected: PASS (E2E cần Postgres; skip nếu không có).

- [ ] **Step 5: Commit**

```bash
git add go-server/internal/api/handler.go go-server/internal/api/handler_test.go go-server/cmd/server/main.go go-server/internal/api/controlplane_test.go
git commit -m "feat(api): wrap inference routes với InferenceAuth + routing UI (/, /chat, /keys)"
```

---

### Task 5: UI — `index.html`, `chat.html` (sửa), `keys.html`

**Files:**
- Create: `ui/index.html` (login)
- Modify: `ui/chat.html` (thêm auth gate + Authorization header + logout + link /keys — file hiện tại 30KB, chỉ chỉnh vài chỗ)
- Create: `ui/keys.html` (quản lý API key)
- Không có Go test mới ở task này — xác minh qua smoke (Step 3) + test routing đã có từ Task 4.

**Interfaces:**
- Consumes: `POST /api/v1/auth/login` → `{access_token}`; `POST /v1/chat/completions` (Bearer); `GET/POST/DELETE /api/v1/api-keys` (Bearer). `localStorage["aif_token"]`.

- [ ] **Step 1: Tạo `ui/index.html`**

```html
<!DOCTYPE html>
<html lang="vi">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AI Factory — Đăng nhập</title>
<style>
  body { font-family: system-ui, sans-serif; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; background: #0f1117; color: #e6e6e6; }
  .card { background: #1a1d27; padding: 32px; border-radius: 12px; width: 320px; box-shadow: 0 8px 30px rgba(0,0,0,.4); }
  h1 { font-size: 18px; margin: 0 0 20px; }
  input { width: 100%; box-sizing: border-box; padding: 10px; margin-bottom: 12px; border-radius: 6px; border: 1px solid #333; background: #0f1117; color: #e6e6e6; }
  button { width: 100%; padding: 10px; border: 0; border-radius: 6px; background: #4f8cff; color: #fff; font-weight: 600; cursor: pointer; }
  button:hover { background: #3b77e6; }
  #err { color: #ff6b6b; font-size: 13px; min-height: 18px; margin-top: 8px; }
  .hint { font-size: 12px; color: #888; margin-top: 12px; text-align: center; }
</style>
</head>
<body>
  <form class="card" id="login-form">
    <h1>AI Factory</h1>
    <input id="username" name="username" placeholder="Username" autocomplete="username" required>
    <input id="password" name="password" type="password" placeholder="Password" autocomplete="current-password" required>
    <button type="submit">Đăng nhập</button>
    <div id="err"></div>
    <div class="hint">Demo: <code>admin / admin1234</code></div>
  </form>
<script>
(function () {
  if (localStorage.getItem('aif_token')) { location.href = '/chat'; return; }
  document.getElementById('login-form').addEventListener('submit', async function (e) {
    e.preventDefault();
    const err = document.getElementById('err');
    err.textContent = '';
    try {
      const resp = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: document.getElementById('username').value.trim(), password: document.getElementById('password').value })
      });
      const data = await resp.json();
      if (!resp.ok) { err.textContent = (data.error && data.error.message) || 'Đăng nhập thất bại'; return; }
      localStorage.setItem('aif_token', data.access_token);
      location.href = '/chat';
    } catch (ex) { err.textContent = 'Lỗi kết nối: ' + ex.message; }
  });
})();
</script>
</body>
</html>
```

- [ ] **Step 2: Sửa `ui/chat.html`** — 3 chỉnh sửa trên file hiện tại:

**(a) Auth gate** — chèn vào **đầu** khối `<script>` (ngay sau `<script>` hiện tại, trước `let sessionId`):

```js
if (!localStorage.getItem('aif_token')) { location.href = '/'; throw new Error('redirect'); }
```

**(b) Authorization header** — tìm dòng fetch (hiện tại `const resp = await fetch(url, {` với headers dòng tiếp theo):

```js
      headers: { 'Content-Type': 'application/json', 'x-session-id': sessionId },
```
đổi thành:
```js
      headers: { 'Content-Type': 'application/json', 'x-session-id': sessionId, 'Authorization': 'Bearer ' + localStorage.getItem('aif_token') },
```

**(c) Logout + link /keys** — thêm 1 nút trong header/nav của trang (cạnh chỗ hiển thị session-id). Chèn HTML sau dòng có `session-id` span (vị trí linh hoạt, miễn trong header):

```html
<button id="logout-btn" type="button" style="margin-left:8px">Đăng xuất</button>
<a href="/keys" style="margin-left:8px;color:#4f8cff">API keys</a>
```

Và cuối khối `<script>` thêm:

```js
document.getElementById('logout-btn').addEventListener('click', function () {
  localStorage.removeItem('aif_token');
  location.href = '/';
});
```

Nếu `chat.html` không có sẵn vị trí header phù hợp, implementer có thể đặt nút logout + link trong một `<div>` cố định góc phải — miễn là (1) redirect khi thiếu token, (2) Authorization header đúng, (3) có logout + link /keys.

- [ ] **Step 3: Tạo `ui/keys.html`**

```html
<!DOCTYPE html>
<html lang="vi">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AI Factory — API Keys</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 0; background: #0f1117; color: #e6e6e6; padding: 24px; }
  .wrap { max-width: 720px; margin: 0 auto; }
  header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }
  h1 { font-size: 20px; margin: 0; }
  a { color: #4f8cff; text-decoration: none; }
  button { padding: 8px 14px; border: 0; border-radius: 6px; background: #4f8cff; color: #fff; cursor: pointer; }
  button.danger { background: #c0392b; }
  .row { display: flex; gap: 8px; margin-bottom: 16px; }
  input { padding: 8px; border-radius: 6px; border: 1px solid #333; background: #1a1d27; color: #e6e6e6; flex: 1; }
  table { width: 100%; border-collapse: collapse; }
  td, th { text-align: left; padding: 8px; border-bottom: 1px solid #222; font-size: 14px; }
  #raw-box { background: #1a1d27; border: 1px solid #333; border-radius: 8px; padding: 12px; margin: 12px 0; display: none; }
  #raw-box code { word-break: break-all; color: #7ee787; }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <h1>API Keys</h1>
    <nav><a href="/chat">Chat</a> &nbsp; <a href="#" id="logout">Đăng xuất</a></nav>
  </header>
  <div class="row">
    <input id="name" placeholder="Tên key (vd: local-dev)">
    <button id="create">Tạo key</button>
  </div>
  <div id="raw-box">Key mới (chỉ hiện 1 lần): <code id="raw-key"></code>
    <button id="copy" style="margin-left:8px">Copy</button>
  </div>
  <table>
    <thead><tr><th>Name</th><th>Status</th><th>Created</th><th>Expires</th><th></th></tr></thead>
    <tbody id="rows"></tbody>
  </table>
</div>
<script>
(function () {
  const token = localStorage.getItem('aif_token');
  if (!token) { location.href = '/'; return; }
  const H = { 'Authorization': 'Bearer ' + token };
  async function api(path, opts) {
    const resp = await fetch(path, Object.assign({ headers: H }, opts));
    if (resp.status === 401) { location.href = '/'; throw new Error('unauthorized'); }
    return resp;
  }
  function esc(s) { const d = document.createElement('div'); d.textContent = String(s); return d.innerHTML; }
  async function load() {
    const resp = await api('/api/v1/api-keys');
    const keys = await resp.json();
    const rows = document.getElementById('rows');
    rows.innerHTML = '';
    for (const k of keys) {
      const tr = document.createElement('tr');
      tr.innerHTML = '<td>' + esc(k.name) + '</td><td>' + esc(k.status) + '</td><td>' + esc(k.created_at || '') + '</td><td>' + esc(k.expires_at || '-') + '</td>';
      const td = document.createElement('td');
      const del = document.createElement('button');
      del.className = 'danger'; del.textContent = 'Revoke';
      del.onclick = async function () { await api('/api/v1/api-keys/' + k.id, { method: 'DELETE' }); load(); };
      td.appendChild(del); tr.appendChild(td);
      rows.appendChild(tr);
    }
  }
  document.getElementById('create').onclick = async function () {
    const name = document.getElementById('name').value.trim();
    if (!name) return;
    const resp = await api('/api/v1/api-keys', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: name }) });
    const data = await resp.json();
    const box = document.getElementById('raw-box');
    box.style.display = 'block';
    document.getElementById('raw-key').textContent = data.key;
  };
  document.getElementById('copy').onclick = function () {
    navigator.clipboard.writeText(document.getElementById('raw-key').textContent);
  };
  document.getElementById('logout').onclick = function () { localStorage.removeItem('aif_token'); location.href = '/'; };
  load();
})();
</script>
</body>
</html>
```

- [ ] **Step 4: Smoke test thủ công** (cần Postgres chạy; worker chỉ cần khi chat):

Terminal 1: `cd go-server && go run ./cmd/server/` (Postgres phải sẵn sàng)
Terminal 2:
```bash
# login lấy token
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
# các trang serve đúng
curl -s http://localhost:8080/ | grep -q "Đăng nhập" && echo "OK index"
curl -s http://localhost:8080/chat | grep -qi "chat" && echo "OK chat page"
curl -s http://localhost:8080/keys | grep -q "API Keys" && echo "OK keys page"
# chat không auth → 401
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"qwen-3b","messages":[{"role":"user","content":"hi"}]}'
#   → phải ra 401
# tạo + xoá key qua UI flow (hoặc curl):
curl -s -X POST http://localhost:8080/api/v1/api-keys -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"smoke"}'
```
Browser: `http://localhost:8080/` → login `admin/admin1234` → chat gửi tin nhắn → `/keys` tạo + revoke key → logout.
Expected: toàn bộ flow chạy.

- [ ] **Step 5: Commit**

```bash
git add ui/index.html ui/chat.html ui/keys.html
git commit -m "feat(ui): login, chat (auth), API key management pages"
```

---

### Task 6: Docs — CLAUDE.md + TRACKING.md

**Files:**
- Modify: `CLAUDE.md` (Key Decisions/Known Gaps/Running)
- Modify: `docs/TRACKING.md` (việc treo + nhật ký)

**Interfaces:** không — chỉ docs.

- [ ] **Step 1: Cập nhật `CLAUDE.md`**

(a) Trong mục **Known Gaps**, dòng cuối:
```
- **Not yet:** auth/rate-limit/persistence, sandbox for `run_command`, observability (metrics/tracing/cost).
```
đổi thành:
```
- **Not yet:** rate-limit/persistence, sandbox for `run_command`, observability (usage/tracing/cost).
- **Auth trên inference đã có** (consumer slice): `/v1/chat/completions` + `/v1/messages` yêu cầu `Authorization: Bearer <JWT hoặc API key>`; UI 3 trang login/chat/keys. Chi tiết `docs/superpowers/specs/2026-08-15-consumer-auth-ui-design.md`.
```

(b) Trong mục **Running**, dòng curl test Anthropic — thêm ghi chú auth:

Sau dòng:
```
# Quick test (Anthropic adapter) — NOTE: content must be an ARRAY of content blocks
```
chèn:
```
# NOTE: inference endpoints now require auth. Login first, then pass the JWT (or an API key):
#   TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
#     -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
```

Và trong ví dụ curl, thêm header:
```
  -H "Authorization: Bearer $TOKEN" \
```

- [ ] **Step 2: Cập nhật `docs/TRACKING.md`**

(a) Trong mục **⚠️ Việc treo**, dòng:
```
- [ ] Chưa có: auth / rate-limit / persistence, sandbox cho `run_command`, observability (metrics/tracing/cost).
```
đổi thành:
```
- [x] **Auth trên inference** (consumer slice): JWT + API key bắt buộc trên `/v1/chat/completions` + `/v1/messages`; UI login/chat/keys.
- [ ] Chưa có: rate-limit / persistence, sandbox cho `run_command`, observability (usage/tracing/cost).
```

(b) Trong **Nhật ký cập nhật**, thêm dòng:
```
| 2026-08-15 | Consumer slice: auth trên inference (JWT/API key) + UI 3 trang; hoãn M2 Task 2–6; spec `docs/superpowers/specs/2026-08-15-consumer-auth-ui-design.md`. |
```

- [ ] **Step 3: Xác minh toàn cục**

Run: `cd go-server && go vet ./... && go test ./...`
Expected: tất cả pass (các test DB-gated skip nếu không có Postgres). Working tree chỉ còn file docs này được sửa.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/TRACKING.md
git commit -m "docs: consumer slice — auth trên inference + UI, cập nhật gaps/running/tracking"
```
