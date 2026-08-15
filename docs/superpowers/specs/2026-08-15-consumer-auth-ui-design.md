# Design — Consumer slice: Auth trên inference + UI (login / chat / API keys)

- **Ngày**: 2026-08-15
- **Trạng thái**: Đã duyệt qua brainstorming (user duyệt design 5 phần) — chờ review spec trước khi lập kế hoạch
- **Phạm vi**: Gắn auth (JWT **hoặc** API key) vào 2 endpoint inference `/v1/chat/completions` + `/v1/messages`; bổ sung list/revoke API key ở control plane; dựng UI 3 trang (login → chat + quản lý API key) bằng vanilla HTML/JS do Go serve tĩnh. Mục tiêu trải nghiệm: **"người dùng đăng nhập để dùng model"** và **"lấy API key để gọi model"**.
- **Liên hệ spec nền**: xây trên `2026-08-15-serving-platform-design.md`. Slice này **cố ý đi lệch D4** (spec gốc ghi "không chat UI trong M1–M4") và **front-load phần API-key auth của M3**; UI là bổ sung của riêng user, không thay đổi lộ trình M1–M4.

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng đã xác minh (từ code)

- **Đường gọi model KHÔNG auth**: `handler.go:40-41` đăng ký `/v1/chat/completions` (`handleOpenAIChatCompletions`) và `/v1/messages` (`handleAnthropicMessages`) bằng `mux.HandleFunc` trần, không bọc middleware nào. Ai gọi cũng chạy, không cần login/key.
- **Control plane có sẵn**: `POST /api/v1/auth/login` (bare, trả `{"access_token": ...}`), `POST /api/v1/api-keys` (gated `key.manage`, trả `{id, tenant_id, name, key}` với `key` = raw hiện đúng 1 lần). **Chưa có** `GET`/`DELETE` cho api-keys.
- **Primitives auth sẵn có**:
  - `auth.Service` (`service.go`): `Login(ctx, username, password) (string, error)` (JWT), `AuthenticateAPIKey(ctx, rawKey) (*APIKey, error)` (hash + status `ACTIVE` + expiry). `Store` interface chỉ cần `GetUserByUsername` + `GetAPIKeyByHash`.
  - `auth.ParseToken(secret, token) (*Claims, error)`, `auth.IssueToken(...)`, `auth.Claims{UserID, TenantID, Role}` (`json:"uid"|"tid"|"role"`), `ClaimsFromContext(ctx)`.
  - `auth.RequireAuth(secret)` (Bearer JWT → claims vào ctx), `auth.RequirePermission(secret, action)`, `writeAuthError(w, status, code, msg)` → `{"error":{"code":...,"message":...}}`.
  - `auth.GenerateAPIKey() (raw, hash)`, `HashAPIKey(raw)`.
- **Bảng `api_keys`** (migration 0001): `id, tenant_id FK, name, key_hash UNIQUE, status DEFAULT 'ACTIVE', expires_at, last_used_at, created_at`. Struct `controlplane.APIKey{ID, TenantID, Name, Status, ExpiresAt, CreatedAt}` (json tags tương ứng).
- **`controlplane.Service`** (`users.go`): `CreateAPIKey`, `GetAPIKeyByHash`. **Chưa có** `ListAPIKeys` / `DeleteAPIKey`.
- **Wiring** (`cmd/server/main.go`): `authSvc := auth.NewService(cp, secret, 15min)` (dòng 59) đã tồn tại; `api.NewHandler(sessionMgr, loop, dir)` (dòng 103) **chưa nhận** authSvc/secret.
- **UI tĩnh**: `handleUI` (`handler.go:499`) serve `chat.html` cho mọi path `/`; `/concepts` → `concepts.html`. `corsMiddleware` đã cho phép header `Authorization`.
- **RBAC**: `key.manage` đã có trong rolePermissions của `admin` + operator — GET/DELETE api-keys gated `key.manage` không cần thêm quyền.

### 1.2 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Auth cả 2 endpoint inference** `/v1/chat/completions` + `/v1/messages`; bắt buộc, bỏ truy cập ẩn danh. |
| D2 | **Chấp nhận 2 loại token**: JWT (đăng nhập) **hoặc** API key. Valid principal = được gọi; **không thêm RBAC action mới** (API key không có trường permission). |
| D3 | **UI vanilla HTML/JS**, Go serve tĩnh (giữ pattern hiện tại, không build step). 3 trang: `index.html` (login), `chat.html` (chat, thay bản test cũ), `keys.html` (quản lý API key). |
| D4 | **Scope: consumer slice** — không admin UI, không create-account (tạo user là việc control plane). |
| D5 | **Revoke key = hard delete** (bảng `api_keys` không có FK con; đơn giản). |
| D6 | **Hoãn M2 Task 2–6** (events package Task 1 giữ nguyên, đã commit). |
| D7 | **Session giữ nguyên** keyed-by-`x-session-id`, chưa scope theo tenant. |
| D8 | **Không usage/quota tracking** trong slice này (defer M3/M4), kể cả `last_used_at` (cập nhật thuộc telemetry, defer M3). |

---

## 2. Kiến trúc

```
Client (UI / API key holder)
   │  Authorization: Bearer <JWT | api-key>
   ▼
auth.InferenceAuth(secret, authSvc)   ← middleware mới, bọc 2 route inference
   ├─ ParseToken(secret, tok) ok?      → Claims vào ctx (đường JWT, reuse ctxKey hiện có)
   │     └─ tiếp tục handler
   ├─ else AuthenticateAPIKey(ctx, tok) ok? → APIKey vào ctx (ctx key mới)
   │     └─ tiếp tục handler
   └─ cả hai fail → 401 / 403 writeAuthError

Control plane bổ sung: GET /api/v1/api-keys, DELETE /api/v1/api-keys/{id} (gated key.manage)

UI (Go serve tĩnh):
   /        → index.html  (login)
   /chat    → chat.html   (gọi /v1/chat/completions kèm Bearer JWT)
   /keys    → keys.html   (list/tạo/revoke API key kèm Bearer JWT)
```

---

## 3. Auth middleware — `auth.InferenceAuth`

**File**: `go-server/internal/auth/middleware.go` (đặt cạnh `RequireAuth`/`RequirePermission`).

**Signature**: `func InferenceAuth(secret []byte, svc *Service) func(http.Handler) http.Handler`

**Luồng xử lý** (nhận `Authorization: Bearer <tok>`):

1. Thiếu header hoặc không đúng prefix `Bearer ` → `writeAuthError(w, 401, "UNAUTHORIZED", "missing bearer token")`.
2. Thử `ParseToken(secret, tok)`:
   - ok → `ctx = context.WithValue(r.Context(), ctxKey{}, claims)` (reuse `ctxKey` hiện có, để `ClaimsFromContext` vẫn hoạt động) → `next`.
   - fail → chuyển bước 3.
3. Thử `svc.AuthenticateAPIKey(ctx, tok)`:
   - ok → `ctx = context.WithValue(ctx, apiKeyCtxKey{}, key)` → `next`.
   - `ErrInvalidAPIKey` → `writeAuthError(w, 401, "UNAUTHORIZED", "invalid API key")`.
   - `ErrKeyInactive` (inactive/expired) → `writeAuthError(w, 403, "FORBIDDEN", "API key inactive or expired")`.
4. **Không log token/key thô** — middleware chỉ log nếu cần, và chỉ log prefix/ID, tuyệt đối không log giá trị token (rule dự án).

**Context**: thêm `apiKeyCtxKey struct{}` + `func APIKeyFromContext(ctx context.Context) (*APIKey, bool)` trong `auth/middleware.go` (đối xứng `ClaimsFromContext`). Handler inference có thể đọc tenant từ `ClaimsFromContext` (JWT) hoặc `APIKeyFromContext` (key) — slice này chưa dùng để scope session, nhưng để sẵn cho M3.

**Yêu cầu bắt buộc**: không log token/key, không trả token trong response ngoài đúng điểm tạo key.

---

## 4. Control plane: list + revoke API key

**`controlplane.Service`** (`users.go`, thêm sau `GetAPIKeyByHash`):

```go
// ListAPIKeys trả các key của tenant, mới nhất trước.
func (s *Service) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
    // SELECT id, tenant_id, name, status, expires_at, created_at
    // FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC
}

// DeleteAPIKey xoá key theo id + tenant (scoping an toàn). Lỗi nếu không khớp.
func (s *Service) DeleteAPIKey(ctx context.Context, id, tenantID string) error {
    // DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2
    // nếu RowsAffected != 1 → ErrNotFound
}
```

Package `controlplane` hiện chỉ có `ErrInvalidTransition` (deployment.go) — **thêm mới** `var ErrNotFound = errors.New("not found")` ở `controlplane/users.go` (gần vùng api keys; thêm import `errors`).

**Handlers** (`api/controlplane.go`, gated `key.manage` như `POST /api/v1/api-keys`):

| Route | Handler | Response |
|---|---|---|
| `GET /api/v1/api-keys` | `handleListAPIKeys` | `200 []APIKey` (bare array, giống `ListTenants`) |
| `DELETE /api/v1/api-keys/{id}` | `handleDeleteAPIKey` | `204 No Content`; id không thuộc tenant → `404 {"error":{"code":"NOT_FOUND",...}}` |

- Lấy `tenantID` từ `auth.ClaimsFromContext` (middleware `RequirePermission` đã gắn claims).
- `DELETE`: id lấy từ `strings.TrimPrefix(r.URL.Path, "/api/v1/api-keys/")`.

---

## 5. UI — 3 trang vanilla

Tất cả inline CSS/JS, không build. Go serve tĩnh từ `uiDir`.

### 5.1 Routing (`api/handler.go`)

`handleUI` hiện serve `chat.html` cho mọi `/` → đổi thành route theo path:

| Path | File |
|---|---|
| `/` | `index.html` |
| `/chat` | `chat.html` |
| `/keys` | `keys.html` |
| khác | `http.NotFound` — giữ hành vi 404 hiện tại của `handleUI` (bỏ alias cũ `/ui`, `/ui/`) |

Giữ `/concepts` → `concepts.html` như cũ.

### 5.2 `ui/index.html` — Login

- Form username/password → `POST /api/v1/auth/login` `{username, password}` → nhận `{access_token}` → lưu `localStorage["aif_token"]` → `location.href = "/chat"`.
- Lỗi 401 → hiện thông báo. Đã có token → redirect thẳng `/chat`.
- Không có "create account". Gợi ý demo: `admin / admin1234` (seed sẵn).

### 5.3 `ui/chat.html` — Chat (thay bản test cũ)

- Load: đọc `localStorage["aif_token"]`; không có → redirect `/`.
- Gửi: `POST /v1/chat/completions` với header `Authorization: Bearer <token>` + `x-session-id` (giữ logic SSE render + session hiện có từ chat.html cũ — tái dùng phần lớn).
- Header: nút Logout (xoá token → `/`), link "API keys" → `/keys`.

### 5.4 `ui/keys.html` — Quản lý API key

- Load: đọc token; không có → redirect `/`.
- `GET /api/v1/api-keys` → render bảng (name, created_at, expires_at, status).
- Form tạo: name (+ expires_at optional) → `POST /api/v1/api-keys` → **hiện raw key đúng 1 lần** (kèm nút copy; cảnh báo không hiện lại).
- Mỗi dòng: nút Revoke → `DELETE /api/v1/api-keys/{id}` → refresh list.
- Lỗi 401 → redirect `/`.

---

## 6. API surface (thay đổi)

| Route | Auth | Thay đổi |
|---|---|---|
| `POST /v1/chat/completions` | `InferenceAuth` | **Mới**: trước đây mở trần |
| `POST /v1/messages` | `InferenceAuth` | **Mới**: trước đây mở trần |
| `GET /api/v1/api-keys` | `key.manage` | **Mới** |
| `DELETE /api/v1/api-keys/{id}` | `key.manage` | **Mới** |

Lỗi auth dùng cùng shape `{"error":{"code":...,"message":...}}` (đã có `writeAuthError`).

---

## 7. Scope cut / non-goals (chốt rõ)

- ❌ Không usage/quota tracking, không cập nhật `last_used_at` (defer M3/M4).
- ❌ Không admin UI, không create-account.
- ❌ Không nối deployment vào đường inference (defer M3).
- ❌ Không RBAC action mới; không rate-limit; không tenant-scope session.
- ❌ M2 Task 2–6 hoãn (events package giữ nguyên).
- ⚠️ **Hệ quả chấp nhận**: mở thẳng `ui/chat.html` (file://) sẽ không dùng được nữa vì thiếu token — demo phải qua `/chat` sau login. Bản test UI cũ bị thay.

---

## 8. Testing

1. **`auth/middleware_test.go`** — unit `InferenceAuth` với fake `Store` (chỉ cần `GetAPIKeyByHash`):
   - JWT hợp lệ → next chạy, `ClaimsFromContext` đúng.
   - API key hợp lệ → next chạy, `APIKeyFromContext` đúng (tenant/status).
   - Thiếu header / prefix sai → 401.
   - Token không hợp lệ (không phải JWT, không phải key) → 401.
   - Key inactive/expired → 403.
   - Assert response JSON shape `{"error":{...}}`.
2. **`controlplane/users_test.go`** — `ListAPIKeys` (đúng tenant, mới nhất trước), `DeleteAPIKey` (thành công + id không thuộc tenant → ErrNotFound).
3. **E2E `api/controlplane_test.go`** (gate `AI_FACTORY_DATABASE_URL`, theo mẫu `TestLoginE2E`):
   - Login → `GET /api/v1/api-keys` → 200; tạo key → `DELETE` → 204 → list rỗng.
   - `POST /v1/chat/completions` **không auth** → **401** (chạy được không cần worker, vì middleware chặn trước khi vào handler). Path "có auth → vào handler" do unit test middleware phủ.
4. **`go vet ./...`** + `go test ./...` xanh (mọi test Postgres-gated skip khi không có DB).

---

## 9. Files touched

- `go-server/internal/auth/middleware.go` — thêm `InferenceAuth` + `apiKeyCtxKey` + `APIKeyFromContext`.
- `go-server/internal/auth/middleware_test.go` — unit test middleware.
- `go-server/internal/controlplane/users.go` — `ListAPIKeys`, `DeleteAPIKey` (+ `ErrNotFound` nếu chưa có).
- `go-server/internal/controlplane/users_test.go` — test 2 method trên.
- `go-server/internal/api/controlplane.go` — 2 route mới + handler.
- `go-server/internal/api/controlplane_test.go` — E2E gated.
- `go-server/internal/api/handler.go` — `NewHandler` thêm `authSvc *auth.Service` + `secret []byte`; bọc 2 route inference bằng `InferenceAuth`; `handleUI` route theo path.
- `go-server/cmd/server/main.go` — truyền `authSvc` + secret vào `api.NewHandler`.
- `ui/index.html`, `ui/chat.html` (thay), `ui/keys.html`.
- `CLAUDE.md`, `docs/TRACKING.md` — cập nhật (auth đã có trên inference; demo phải qua login; thay dòng "chưa có: auth" trong việc treo).
