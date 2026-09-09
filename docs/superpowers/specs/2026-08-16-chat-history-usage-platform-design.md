# Design — Chat history UI (kiểu ChatGPT) + Platform console (API keys + usage)

- **Ngày**: 2026-08-16
- **Trạng thái**: Đã duyệt qua brainstorming (user duyệt design toàn bộ) — chờ review spec trước khi lập kế hoạch
- **Phạm vi**: (1) sidebar lịch sử chat kiểu ChatGPT (list / chọn / đổi tên / xoá hội thoại); (2) persist usage theo từng request vào Postgres rồi hiển thị thống kê; (3) gộp trang Platform thành console cho user thường (tabs Usage + API Keys), chuyển các tab infra ra trang riêng `/infra`. Mục tiêu trải nghiệm: **bắt chước DeepSeek / ChatGPT** — chat có lịch sử, phần quản lý key + usage tách khỏi chat.
- **Liên hệ spec nền**: xây trên `2026-08-15-consumer-auth-ui-design.md` (auth JWT/API-key đã có) và migration `0004_chat_history.sql` (persistence session/message đã có nhưng chưa có endpoint list + title). Phần UI là app NextJS ở `web/` (không phải `ui/` vanilla).

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng đã xác minh (từ code)

- **Persistence chat history đã có** (`go-server/internal/session/store.go`, migration `0004`): bảng `sessions(id TEXT PK, tenant_id UUID, user_id UUID NULL, model, system_prompt, max_tokens, created_at, updated_at)` + `messages(session_id, seq, role, content, tool_calls, tool_call_id, tool_result, is_error)`. `PGStore` implement `Store` interface: `UpsertSession`, `LoadSession`, `AppendMessage`; lỗi `ErrSessionNotFound`, `ErrSessionForbidden`.
- **`Session` struct** (`session.go`): `ID, TenantID, UserID, Model, SystemPrompt, MaxTokens, Messages, CreatedAt, UpdatedAt, Metadata` + nội bộ `store`, `seq`, `persisted`. `AddMessage(ctx, msg)` append + persist best-effort (log warn, không fail turn). `Manager.GetOrCreate(ctx, sessionID, tenantID, userID)` lazy-load từ DB khi miss.
- **Endpoint session hiện tại là memory-only + chưa auth**: `api/handler.go` → `mux.HandleFunc("/v1/sessions/", h.handleSessions)`; `handleSessions` gọi `h.sessionMgr.Get(sessionID)` (chỉ memory) — **không đọc DB**, **không filter tenant**. Không có endpoint list/rename/delete session.
- **Usage hiện chỉ là Prometheus counter in-memory**: `observability.RecordTokenUsage(tenantID, modelID, prompt, completion)` (trong `api/handler.go` tại 2 nhánh `LoopEventFinal` của stream + non-stream) ghi vào `TokensTotal` (label `tenant/model/type`). **Mất khi restart**, không có bảng DB, không có endpoint đọc usage.
- **Quota** (`controlplane/quota.go`, bảng `tenant_quotas`): chỉ lưu `limit_value` (giới hạn), **không theo dõi usage thực tế**, không enforce.
- **API keys** đã đủ: `POST/GET /api/v1/api-keys`, `DELETE /api/v1/api-keys/{id}` (gated `key.manage`). UI ở `web/src/app/(app)/keys/page.tsx`.
- **RBAC**: `ActionUsageRead = "usage.read"` đã có, và **cả 4 role** (`PLATFORM_ADMIN`, `TENANT_ADMIN`, `TENANT_DEVELOPER`, `TENANT_VIEWER`) đều được cấp `usage.read`.
- **Auth middleware**: `auth.RequireAuth(secret)` (JWT → `Claims` vào ctx, **chỉ JWT** — không nhận API key), `auth.RequirePermission(secret, action)`.
- **Frontend NextJS** (`web/src/`): nav `Sidebar.tsx` = Chat / API Keys / Platform / (Admin). `/platform` hiện là trang infra (Deployments/Models/Templates/Quotas). `ChatClient.tsx` quản lý **một** session qua `localStorage["aif_session"]`, restore bằng `GET /v1/sessions/{id}`. Proxy `next.config.ts` đã rewrite `/api/v1/:path*` + `/v1/:path*` → Go (endpoint mới tự chạy qua proxy). Không có chart lib (chỉ `react-markdown` + `remark-gfm`).

### 1.2 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Usage lưu vào bảng `usage_events` (Postgres)**, append-only 1 dòng/request, aggregate bằng SQL. Không dùng Prometheus làm nguồn chính (mất khi restart). |
| D2 | **Trang Platform tối giản cho user thường** = tabs [Usage, API Keys]. Deployments/Models/Templates/Quotas chuyển ra trang `/infra` (tenant-scoped, giữ nguyên permission hiện tại). |
| D3 | **Sidebar chat đầy đủ** kiểu ChatGPT: list (tiêu đề tự sinh + thời gian), chọn/xem, đổi tên, xoá, "Chat mới". |
| D4 | **Session scope theo tenant** (nhất quán luồng chat hiện tại), **không chia per-user**. Ghi nhận là giới hạn đã biết. |
| D5 | **Code usage đặt trong `controlplane` package** (đã có pool DB + khái niệm quota), không tạo package mới. |
| D6 | **Biểu đồ dùng inline SVG/div** (không thêm thư viện chart) — giữ zero dependency mới. |
| D7 | **Ghi usage best-effort**: lỗi DB chỉ log warn, không fail lượt chat. |
| D8 | **Không enforce quota, không tính chi phí** — slice này chỉ *hiển thị* usage (enforce thuộc roadmap M3/M4 riêng). |

---

## 2. Kiến trúc

```
Client (NextJS web/)
  ├─ /chat  → ChatClient + SessionsSidebar
  │             └─ GET /api/v1/sessions            (list, RequireAuth, tenant-scoped)
  │                GET /api/v1/sessions/{id}       (messages)
  │                PATCH /api/v1/sessions/{id}     (rename)
  │                DELETE /api/v1/sessions/{id}    (delete)
  │                POST /v1/chat/completions       (như cũ; mỗi lần final → ghi usage)
  ├─ /platform → [Usage, API Keys]
  │             └─ GET /api/v1/usage               (summary/daily/by_model, usage.read)
  │                /api/v1/api-keys (như cũ, dời từ /keys)
  └─ /infra → Deployments/Models/Templates/Quotas  (rename route /platform cũ)

Backend (Go):
  api.Handler ── sessionMgr (memory+DB) ── session.Store (mở rộng: List/Rename/Delete + title)
  api.Handler ── usage: UsageRecorder ───── controlplane.Service.RecordUsage (INSERT usage_events)
  ControlPlaneHandler ── handleGetUsage ─── controlplane.Service.UsageSummary/Daily/ByModel
```

---

## 3. Data model — 2 migration mới

### `0005_session_title.sql`

```sql
-- +goose Up
ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE sessions DROP COLUMN title;
```

### `0006_usage_events.sql`

```sql
-- +goose Up
CREATE TABLE usage_events (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    model             TEXT NOT NULL,
    prompt_tokens     INT  NOT NULL DEFAULT 0,
    completion_tokens INT  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX usage_events_tenant_created_idx ON usage_events (tenant_id, created_at DESC);
-- +goose Down
DROP TABLE IF EXISTS usage_events;
```

> **Ghi chú pgx**: cột `prompt_tokens`/`completion_tokens` là `INT` (int4) → khi INSERT dùng cast `::int` (Go `int` được pgx map thành int8); `tenant_id` là string UUID → cast `::uuid`. Đây là đúng lỗi type-mapping đã gặp ở `store.go` (SQLSTATE 42804). Aggregate `SUM(...)`/`COUNT(*)` trả int8 → scan vào `int64`.

---

## 4. Usage tracking — `controlplane/usage.go` (mới)

Types (json tags để trả trực tiếp):

```go
type UsageSummary struct {
    PromptTokens     int64 `json:"prompt_tokens"`
    CompletionTokens int64 `json:"completion_tokens"`
    TotalTokens      int64 `json:"total_tokens"`
    Requests         int64 `json:"requests"`
}

type UsageDailyPoint struct {
    Date     string `json:"date"` // "2006-01-02"
    PromptTokens int64 `json:"prompt_tokens"`
    CompletionTokens int64 `json:"completion_tokens"`
    Requests  int64 `json:"requests"`
}

type UsageByModel struct {
    Model            string `json:"model"`
    PromptTokens     int64  `json:"prompt_tokens"`
    CompletionTokens int64  `json:"completion_tokens"`
    TotalTokens      int64  `json:"total_tokens"`
    Requests         int64  `json:"requests"`
}
```

Methods trên `*Service` (đã có `s.db` pool):

```go
// RecordUsage INSERT 1 dòng usage_events (append-only).
func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
// UsageSummary gộp tổng prompt/completion/total + số request trong [from, to).
func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)
// UsageDaily gộp theo ngày (created_at::date) trong [from, to), mỗi ngày 1 dòng.
func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)
// UsageByModel gộp theo model trong [from, to), sort theo total_tokens DESC.
func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)
```

SQL minh hoạ (đều `WHERE tenant_id = $1::uuid AND created_at >= $2 AND created_at < $3`):

- `RecordUsage`: `INSERT INTO usage_events (tenant_id, model, prompt_tokens, completion_tokens) VALUES ($1::uuid, $2, $3::int, $4::int)`.
- `UsageSummary`: `SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COUNT(*) FROM usage_events WHERE ...` → `TotalTokens = Prompt+Completion`.
- `UsageDaily`: `SELECT created_at::date, SUM(prompt_tokens), SUM(completion_tokens), COUNT(*) ... GROUP BY created_at::date ORDER BY 1`; scan `date` → `time.Time`, format `"2006-01-02"`.
- `UsageByModel`: `SELECT model, SUM(prompt_tokens), SUM(completion_tokens), COUNT(*) ... GROUP BY model ORDER BY SUM(prompt_tokens)+SUM(completion_tokens) DESC`.

---

## 5. Session store mở rộng — `session/store.go`

Thêm vào interface `Store`:

```go
// ListSessions trả metadata các session của tenant, mới cập nhật trước.
ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error)
// RenameSession đổi title, scoped theo tenant.
RenameSession(ctx context.Context, id, tenantID, title string) error
// DeleteSession xoá session, scoped theo tenant.
DeleteSession(ctx context.Context, id, tenantID string) error
```

- `type SessionSummary struct { ID, Title, Model string; CreatedAt, UpdatedAt time.Time; MessageCount int }` (json tags `id/title/model/created_at/updated_at/message_count`).
- `ListSessions` SQL (title fallback cho session cũ chưa có title):

```sql
SELECT s.id,
       COALESCE(NULLIF(s.title,''),
                (SELECT m.content FROM messages m
                 WHERE m.session_id = s.id AND m.role = 'user'
                 ORDER BY m.seq ASC LIMIT 1),
                '') AS title,
       s.model, s.created_at, s.updated_at,
       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS message_count
FROM sessions s
WHERE s.tenant_id = $1::uuid
ORDER BY s.updated_at DESC
```

- `RenameSession`: `UPDATE sessions SET title = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2::uuid`; `RowsAffected() == 0` → `ErrSessionNotFound`.
- `DeleteSession`: `DELETE FROM sessions WHERE id = $1 AND tenant_id = $2::uuid`; `RowsAffected() == 0` → `ErrSessionNotFound` (message cascade xoá).
- **Tự sinh title** trong `AddMessage`: nếu `s.Title == ""` và `msg.Role == RoleUser` → `s.Title = truncate(msg.Content, 40)` (trước khi persist; session mới sẽ ghi title qua `UpsertSession` đầu tiên). Session cũ title rỗng được `COALESCE` lấp ở read-time.
- `UpsertSession` thêm cột `title` vào cả `INSERT` và `ON CONFLICT ... DO UPDATE SET title = EXCLUDED.title`; `LoadSession` SELECT thêm `title`.

`Manager` thêm wrapper mỏng `ListSessions` / `RenameSession` / `DeleteSession` (delegate store; khi store nil → trả lỗi "not persisted").

---

## 6. API surface (thay đổi)

### 6.1 Session resource (đặt trong `api/handler.go`, thay endpoint cũ)

Xoá `mux.HandleFunc("/v1/sessions/", h.handleSessions)` (memory-only). Thêm (dùng `auth.RequireAuth` — **chỉ JWT**, API key không được quản lý session):

| Route | Handler | Response |
|---|---|---|
| `GET /api/v1/sessions` | `handleListSessions` | `200 []SessionSummary` |
| `GET /api/v1/sessions/{id}` | `handleGetSession` | `200 {id, title, model, messages, created_at, updated_at}`; không tìm thấy/khác tenant → `404` |
| `PATCH /api/v1/sessions/{id}` | `handleRenameSession` | body `{title}`; `200 {id, title}`; `404` nếu không khớp |
| `DELETE /api/v1/sessions/{id}` | `handleDeleteSession` | `204`; `404` nếu không khớp |

- Lấy `tenantID` từ `auth.ClaimsFromContext` (middleware `RequireAuth` đã gắn claims).
- Id từ `strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")` (giống pattern `DELETE /api/v1/api-keys/`).
- `handleGetSession` dùng `h.sessionMgr.GetOrCreate`? **Không** — phải đọc thuần DB để không tạo session rỗng. Dùng `store.LoadSession(ctx, id, tenantID)` qua wrapper `Manager.GetPersisted(ctx, id, tenantID)` (mới), trả `ErrSessionNotFound` → `404`. (Tránh `GetOrCreate` vì nó upsert session rỗng khi id chưa tồn tại.)
- Shape lỗi dùng `writeAPIError`/`writeOpenAIError`? — dùng `writeAPIError(w, status, code, msg)` (đã có trong `api/controlplane.go`; cùng package `api`).

### 6.2 Usage endpoint (đặt trong `api/controlplane.go`)

| Route | Auth | Response |
|---|---|---|
| `GET /api/v1/usage` | `usage.read` | `200` (shape dưới) |

Query `?days=7|30` (default 30, clamp [1,90]). Handler tính: `now = time.Now().UTC()`; `todayStart = ngày hôm nay 00:00 UTC`; `monthStart = now-30d`; `dailyStart = now-(days-1)d`.

```json
{
  "today":   { "prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"requests":0 },
  "month":   { ... },
  "daily":   [ { "date":"2026-08-10","prompt_tokens":0,"completion_tokens":0,"requests":0 }, ... ],
  "by_model":[ { "model":"qwen-3b","prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"requests":0 } ]
}
```

- `today` = `UsageSummary(todayStart, now)`; `month` = `UsageSummary(monthStart, now)`; `daily` = `UsageDaily(dailyStart, now)`; `by_model` = `UsageByModel(monthStart, now)`.
- `tenantID` từ `auth.ClaimsFromContext`.

---

## 7. Frontend (NextJS `web/src/`)

### 7.1 `SessionsSidebar` (mới) + `ChatClient` refactor

- Component mới `web/src/components/SessionsSidebar.tsx`:
  - fetch `GET /api/v1/sessions` khi mount + mỗi khi list thay đổi (sau rename/delete/gửi tin nhắn đầu của session mới).
  - render list: tiêu đề (fallback "Hội thoại mới"), thời gian tương đối, hover hiện nút rename (inline input) + delete (confirm).
  - nút "＋ Chat mới" → tạo id mới, clear active, không gọi API (session chỉ tồn tại sau tin nhắn đầu — đúng lazy của backend).
- `ChatClient.tsx`:
  - Nhận `activeSessionId` + callback từ cha; bỏ việc tự quản lý `localStorage["aif_session"]` đơn lẻ → lift lên `ChatPage` (hoặc giữ trong một wrapper client).
  - Bấm 1 session trong sidebar → `GET /api/v1/sessions/{id}` → đổ messages vào view + set active.
  - Khi gửi xong tin nhắn đầu của session mới → refresh list (để session mới xuất hiện).
  - Giữ nguyên luồng SSE `/v1/chat/completions` + render Markdown hiện tại.

### 7.2 `/platform` mới (tabs Usage + API Keys)

- Viết lại `web/src/app/(app)/platform/page.tsx` thành container 2 tabs.
- **Usage tab** (component `UsageTab`): 4 card (token hôm nay, request hôm nay, token 30 ngày, request 30 ngày), tỷ lệ prompt/completion (1 thanh ngang), biểu đồ cột theo ngày (toggle 7d/30d, **inline div/SVG**, không lib), bảng per-model. Data từ `GET /api/v1/usage`.
- **API Keys tab**: dời nguyên phần thân `keys/page.tsx` hiện tại vào (giữ form tạo + bảng + revoke). Xoá route `/keys` (hoặc redirect → `/platform`).

### 7.3 `/infra` mới

- Đổi tên route `web/src/app/(app)/platform/` → `web/src/app/(app)/infra/` (giữ nguyên nội dung Deployments/Models/Templates/Quotas).

### 7.4 `Sidebar.tsx`

- Nav = Chat (`/chat`), Platform (`/platform`), Infrastructure (`/infra`), Admin (`/admin`, platform-admin only). Bỏ item "API Keys" riêng.
- `/infra` hiển thị cho mọi user đã đăng nhập (backend đã chặn per-endpoint bằng RBAC; viewer chỉ đọc).

### 7.5 Types + api helper

- Thêm types vào `web/src/lib/types.ts`: `SessionSummary`, `UsageSummary`, `UsageDailyPoint`, `UsageByModel`, `UsageResponse`.
- Không cần thay `api.ts` (dùng `apiFetch` sẵn có).

---

## 8. Bảo mật / xử lý lỗi

- **Mọi endpoint session + usage đều tenant-scoped**: id lấy từ path không được dùng để truy cập chéo tenant — SQL luôn `AND tenant_id = $n::uuid`, khác tenant trả `404` (không rò id tồn tại).
- **Session endpoint dùng `RequireAuth` (JWT only)** — API key (machine client) không được liệt kê/xoá hội thoại.
- **Usage endpoint gated `usage.read`** (cả 4 role đều có).
- **Ghi usage best-effort**: `RecordUsage` fail → `slog.Warn`, không ảnh hưởng response chat.
- **Không log raw key/token** (rule dự án đã có).

---

## 9. Scope cut / non-goals

- ❌ Không per-user scoping session (tenant-scoped, ghi nhận giới hạn).
- ❌ Không enforce quota, không tính chi phí ($) — chỉ hiển thị tokens + requests.
- ❌ Không pagination cho list sessions / by-model (tenant nhỏ, trả hết).
- ❌ Không real-time (không websocket) — refresh sau mỗi thao tác.
- ❌ Không backfill `title` cho session cũ (dùng `COALESCE` read-time).
- ❌ Không thêm chart library (inline SVG/div).
- ❌ Không migration dữ liệu Prometheus → DB (usage bắt đầu đếm từ lúc deploy).

---

## 10. Testing

1. **`session/store_test.go`** (mở rộng, gated `AI_FACTORY_DATABASE_URL`): `ListSessions` (đúng tenant, sort, title fallback), `RenameSession` (thành công + khác tenant → `ErrSessionNotFound`), `DeleteSession` (xoá + cascade messages + khác tenant → `ErrSessionNotFound`), auto-title trong `AddMessage`.
2. **`controlplane/usage_test.go`** (mới, gated DB): `RecordUsage` → `UsageSummary`/`UsageDaily`/`UsageByModel` round-trip đúng số (chèn 2 tenant khác nhau, assert chỉ đếm tenant gọi).
3. **`api/controlplane_test.go`** (E2E, gated DB, theo mẫu `TestLoginE2E`): login → `GET /api/v1/usage` → 200 shape đúng; `GET /api/v1/sessions` → 200 (chưa auth → 401).
4. **`go vet ./...` + `go test ./...`** xanh (test Postgres-gated skip khi không có DB).
5. **Frontend**: smoke test thủ công — tạo 2-3 hội thoại → thấy trong sidebar → chọn/đổi tên/xoá; gửi vài request → `/platform` Usage hiện số; `/infra` vẫn hoạt động.

---

## 11. Files touched

- `go-server/internal/db/migrations/0005_session_title.sql` (mới).
- `go-server/internal/db/migrations/0006_usage_events.sql` (mới).
- `go-server/internal/controlplane/usage.go` (mới) — `UsageSummary`/`UsageDailyPoint`/`UsageByModel` + 4 method.
- `go-server/internal/controlplane/usage_test.go` (mới).
- `go-server/internal/session/store.go` — mở rộng `Store` interface + `SessionSummary` + `ListSessions`/`RenameSession`/`DeleteSession` + title trong `UpsertSession`/`LoadSession`.
- `go-server/internal/session/session.go` — auto-title trong `AddMessage` + field `Title`.
- `go-server/internal/session/manager.go` — wrapper `ListSessions`/`RenameSession`/`DeleteSession`/`GetPersisted`.
- `go-server/internal/session/store_test.go` — test mới.
- `go-server/internal/api/handler.go` — xoá `handleSessions` cũ; thêm 4 handler session + field `usage UsageRecorder`; gọi `h.usage.RecordUsage` ở 2 nhánh `LoopEventFinal`.
- `go-server/internal/api/controlplane.go` — `GET /api/v1/usage` + `handleGetUsage`.
- `go-server/internal/api/controlplane_test.go` — E2E usage/sessions.
- `go-server/cmd/server/main.go` — truyền `usage=cp` vào `api.NewHandler`.
- `web/src/components/SessionsSidebar.tsx` (mới).
- `web/src/components/ChatClient.tsx` — lift session state, dùng `SessionsSidebar`.
- `web/src/app/(app)/chat/page.tsx` — bọc layout sidebar + chat.
- `web/src/app/(app)/platform/page.tsx` — viết lại (Usage + API Keys tabs).
- `web/src/app/(app)/infra/page.tsx` — đổi tên từ `platform/page.tsx` (giữ nội dung).
- `web/src/app/(app)/keys/page.tsx` — xoá (dời vào platform) hoặc redirect.
- `web/src/components/Sidebar.tsx` — nav mới.
- `web/src/lib/types.ts` — types mới.
- `CLAUDE.md` / `docs/TRACKING.md` — cập nhật (usage đã persist; platform đổi bố cục).
