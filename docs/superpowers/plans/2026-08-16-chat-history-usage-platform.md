# Chat History + Usage + Platform Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a ChatGPT-style chat history sidebar (list + auto-title + rename + delete), persist per-turn token usage to Postgres, and surface it on a new user-facing "Platform" console (Usage + API Keys tabs), with the infra management page moved to a separate `/infra` route.

**Architecture:** Backend extends two existing seams — `session.Store` (PGStore) gains `title` + `ListSessions`/`RenameSession`/`DeleteSession`, and `controlplane.Service` (already holds the pgx pool + tenant concept) gains `RecordUsage` + `UsageSummary`/`UsageDaily`/`UsageByModel`. Two new goose migrations add the `sessions.title` column and the `usage_events` table. The API layer exposes four JWT-authed session endpoints (list/get/rename/delete) on the `Handler` and one `GET /api/v1/usage` endpoint on the `ControlPlaneHandler`, with the chat handler hooking `RecordUsage` into the existing `LoopEventFinal` branch (best-effort, never fails a turn). The NextJS UI gets a `SessionsSidebar` (controlled by a new `ChatPageClient`), a rewritten `/platform` console, and a renamed `/infra` page.

**Tech Stack:** Go 1.22 (net/http method-pattern mux), pgx/v5 + goose (embedded `//go:embed migrations/*.sql`), NextJS 16 App Router + React 19 + Tailwind v4 (inline div/SVG charts — **no new npm dependency**), Postgres (control plane + chat history + usage).

**Spec:** `docs/superpowers/specs/2026-08-16-chat-history-usage-platform-design.md`

## Global Constraints

- **Never log** API keys, tokens, passwords, or raw prompts (mandatory security rule — applies to all `slog` calls added here).
- **Best-effort persistence:** a chat turn must never fail on a usage/session write. All `RecordUsage` / `AddMessage` errors are logged via `slog` and swallowed.
- **Tenant scoping:** every DB read/write is scoped by `tenantID` from the JWT (`auth.ClaimsFromContext`) or API key. Cross-tenant access must return not-found (never leak another tenant's session/usage).
- **pgx type casts:** in `INSERT … VALUES` expressions, Go `string`→`text`, `int`→`int8` and are **not** implicitly cast to `uuid`/`int4`; cast explicitly: `$1::uuid`, `$3::int`. (In `WHERE col = $n` predicates the column type is inferred, so no cast is needed — see `store.go LoadSession`.)
- **Routing:** use Go 1.22 method-pattern routing — `mux.Handle("GET /api/v1/sessions", …)`, not `mux.HandleFunc`.
- **Auth:** session endpoints use `auth.RequireAuth` (JWT-only); the usage endpoint uses `auth.RequirePermission(secret, auth.ActionUsageRead)` (all 4 roles hold it).
- **UI copy:** Vietnamese labels matching the existing pages (`keys/page.tsx`, `platform/page.tsx`). Chart is inline `<div>` bars — no chart library.
- **Conventional commits** (repo uses `feat(scope): …`, `docs: …`).

---

## File Structure

**Backend (Go):**
- `go-server/internal/db/migrations/0005_session_title.sql` — **new** — `ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`.
- `go-server/internal/db/migrations/0006_usage_events.sql` — **new** — `usage_events` table + index.
- `go-server/internal/controlplane/usage.go` — **new** — `UsageSummary`/`UsageDailyPoint`/`UsageByModel` types + `RecordUsage`/`UsageSummary`/`UsageDaily`/`UsageByModel` methods.
- `go-server/internal/controlplane/usage_test.go` — **new** — integration test.
- `go-server/internal/session/session.go` — **modify** — add `Title` field + auto-title in `AddMessage`.
- `go-server/internal/session/store.go` — **modify** — add `title` to upsert/load + `SessionSummary` + `ListSessions`/`RenameSession`/`DeleteSession`.
- `go-server/internal/session/manager.go` — **modify** — `ListSessions`/`GetPersisted`/`RenameSession`/`DeleteSession` wrappers.
- `go-server/internal/session/store_test.go` — **modify** — extend with list/rename/delete/title tests.
- `go-server/internal/api/handler.go` — **modify** — `UsageRecorder` interface + `usage` field; replace `handleSessions` with 4 auth'd handlers; record usage in `LoopEventFinal`.
- `go-server/internal/api/controlplane.go` — **modify** — `handleGetUsage` + route.
- `go-server/internal/api/session_test.go` — **new** — session endpoints E2E.
- `go-server/internal/api/usage_test.go` — **new** — usage endpoint E2E.
- `go-server/internal/api/controlplane_test.go` — **modify** — update `NewHandler` call site.
- `go-server/cmd/server/main.go` — **modify** — pass `cp` as `usage`.

**Frontend (NextJS):**
- `web/src/lib/types.ts` — **modify** — add `SessionSummary` + usage types.
- `web/src/components/SessionsSidebar.tsx` — **new** — chat history sidebar.
- `web/src/components/ChatClient.tsx` — **modify** — controlled by `sessionId` prop + `onSessionChanged`.
- `web/src/components/ChatPageClient.tsx` — **new** — owns `sessions`/`activeId` state, renders sidebar + chat.
- `web/src/app/(app)/chat/page.tsx` — **modify** — render `ChatPageClient`.
- `web/src/components/ApiKeysTab.tsx` — **new** — API-key management (moved from `/keys`).
- `web/src/app/(app)/platform/page.tsx` — **rewrite** — Usage + API Keys tabs.
- `web/src/app/(app)/infra/page.tsx` — **new (git mv)** — old platform content.
- `web/src/app/(app)/keys/page.tsx` — **delete**.
- `web/src/components/Sidebar.tsx` — **modify** — nav: Chat / Platform / Infra.

**Docs:** `CLAUDE.md`, `docs/TRACKING.md`.

---

### Task 1: Usage persistence (`usage_events` + `controlplane/usage.go`)

**Files:**
- Create: `go-server/internal/db/migrations/0006_usage_events.sql`
- Create: `go-server/internal/controlplane/usage.go`
- Test: `go-server/internal/controlplane/usage_test.go`

**Interfaces:**
- Consumes: `*pgxpool.Pool` on `Service.db` (from `users.go` — `type Service struct { db *pgxpool.Pool }`).
- Produces (used by Task 4):
  - `func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error`
  - `func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)`
  - `func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)`
  - `func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)`
  - Types: `UsageSummary`, `UsageDailyPoint`, `UsageByModel` (JSON-tagged, see code).

- [ ] **Step 1: Write the migration**

`go-server/internal/db/migrations/0006_usage_events.sql`:

```sql
-- +goose Up
-- Per-turn token usage, tenant-scoped, written best-effort after each completion.
CREATE TABLE usage_events (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    model             TEXT NOT NULL DEFAULT '',
    prompt_tokens     INT  NOT NULL DEFAULT 0,
    completion_tokens INT  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usage_events_tenant_created_idx ON usage_events (tenant_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS usage_events CASCADE;
```

- [ ] **Step 2: Write the failing test**

`go-server/internal/controlplane/usage_test.go`:

```go
package controlplane

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/db"
	"github.com/google/uuid"
)

// TestRecordAndQueryUsage runs against a live Postgres (set AI_FACTORY_DATABASE_URL),
// mirroring the other controlplane integration tests.
func TestRecordAndQueryUsage(t *testing.T) {
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
	tenant, err := svc.CreateTenant(ctx, "usage-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	now := time.Now().UTC()
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record #1: %v", err)
	}
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen-3b", 0, 0); err != nil {
		t.Fatalf("record #2: %v", err)
	}
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen3.5-9b", 100, 50); err != nil {
		t.Fatalf("record #3: %v", err)
	}

	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	summary, err := svc.UsageSummary(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.PromptTokens != 110 || summary.CompletionTokens != 55 || summary.TotalTokens != 165 || summary.Requests != 3 {
		t.Fatalf("summary = %+v, want prompt=110 completion=55 total=165 requests=3", summary)
	}

	daily, err := svc.UsageDaily(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("daily = %+v, want exactly 1 day", daily)
	}
	if daily[0].PromptTokens != 110 || daily[0].Requests != 3 {
		t.Fatalf("daily[0] = %+v, want prompt=110 requests=3", daily[0])
	}

	byModel, err := svc.UsageByModel(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("byModel = %+v, want 2 models", byModel)
	}
	// Sorted by total tokens desc → qwen3.5-9b first.
	if byModel[0].Model != "qwen3.5-9b" || byModel[0].TotalTokens != 150 {
		t.Fatalf("byModel[0] = %+v, want qwen3.5-9b total=150", byModel[0])
	}

	// Cross-tenant isolation: a different tenant sees nothing.
	other, _ := svc.CreateTenant(ctx, "usage-other-"+uuid.NewString()[:8])
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, other.ID) })
	empty, err := svc.UsageSummary(ctx, other.ID, from, to)
	if err != nil {
		t.Fatalf("empty summary: %v", err)
	}
	if empty.Requests != 0 || empty.TotalTokens != 0 {
		t.Fatalf("empty summary = %+v, want zero", empty)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run (from `go-server/`):
```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/controlplane/ -run TestRecordAndQueryUsage -v
```
Expected: FAIL — `undefined: Service.RecordUsage` (and the query methods).

- [ ] **Step 4: Write the implementation**

`go-server/internal/controlplane/usage.go`:

```go
package controlplane

import (
	"context"
	"fmt"
	"time"
)

// UsageSummary aggregates token + request counts over a time window.
type UsageSummary struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Requests         int64 `json:"requests"`
}

// UsageDailyPoint is one UTC day's aggregate for the bar chart.
type UsageDailyPoint struct {
	Date             string `json:"date"` // YYYY-MM-DD (UTC)
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Requests         int64  `json:"requests"`
}

// UsageByModel is the per-model aggregate for the breakdown table.
type UsageByModel struct {
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Requests         int64  `json:"requests"`
}

// RecordUsage inserts one usage event. Callers treat this as best-effort and
// log the returned error without failing the chat turn.
func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO usage_events (tenant_id, model, prompt_tokens, completion_tokens)
		VALUES ($1::uuid, $2, $3::int, $4::int)`,
		tenantID, model, promptTokens, completionTokens)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// UsageSummary aggregates usage for the tenant over the half-open window [from, to).
func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	var out UsageSummary
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3`,
		tenantID, from, to).
		Scan(&out.PromptTokens, &out.CompletionTokens, &out.TotalTokens, &out.Requests)
	if err != nil {
		return out, fmt.Errorf("usage summary: %w", err)
	}
	return out, nil
}

// UsageDaily returns per-UTC-day aggregates for the tenant over [from, to).
func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	rows, err := s.db.Query(ctx, `
		SELECT (created_at AT TIME ZONE 'UTC')::date::text AS d,
		       COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY d
		ORDER BY d`, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage daily: %w", err)
	}
	defer rows.Close()
	out := []UsageDailyPoint{}
	for rows.Next() {
		var p UsageDailyPoint
		if err := rows.Scan(&p.Date, &p.PromptTokens, &p.CompletionTokens, &p.Requests); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UsageByModel aggregates usage per model over [from, to), ordered by total tokens desc.
func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	rows, err := s.db.Query(ctx, `
		SELECT model,
		       COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY model
		ORDER BY COALESCE(SUM(prompt_tokens + completion_tokens),0) DESC`, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	defer rows.Close()
	out := []UsageByModel{}
	for rows.Next() {
		var m UsageByModel
		if err := rows.Scan(&m.Model, &m.PromptTokens, &m.CompletionTokens, &m.TotalTokens, &m.Requests); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run test to verify it passes**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/controlplane/ -run TestRecordAndQueryUsage -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go-server/internal/db/migrations/0006_usage_events.sql go-server/internal/controlplane/usage.go go-server/internal/controlplane/usage_test.go
git commit -m "feat(usage): persist per-turn token usage to usage_events"
```

---

### Task 2: Session title + list/rename/delete in the store

**Files:**
- Create: `go-server/internal/db/migrations/0005_session_title.sql`
- Modify: `go-server/internal/session/session.go`
- Modify: `go-server/internal/session/store.go`
- Modify: `go-server/internal/session/manager.go`
- Test: `go-server/internal/session/store_test.go`

**Interfaces:**
- Consumes: `Session` struct, `Store` interface, `Manager` struct (all in `session` package). `PGStore.pool *pgxpool.Pool`.
- Produces (used by Task 3):
  - `type SessionSummary struct { ID, Title, Model string; CreatedAt, UpdatedAt time.Time; MessageCount int }` (JSON-tagged: `id`, `title`, `model`, `created_at`, `updated_at`, `message_count`).
  - `func (m *Manager) ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error)`
  - `func (m *Manager) GetPersisted(ctx context.Context, id, tenantID string) (*Session, error)`
  - `func (m *Manager) RenameSession(ctx context.Context, id, tenantID, title string) error`
  - `func (m *Manager) DeleteSession(ctx context.Context, id, tenantID string) error`
  - `Session.Title string` (JSON `title,omitempty`).

- [ ] **Step 1: Write the migration**

`go-server/internal/db/migrations/0005_session_title.sql`:

```sql
-- +goose Up
-- Auto-generated (or user-renamed) display title for the chat sidebar.
ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN IF EXISTS title;
```

- [ ] **Step 2: Write the failing test**

Append to `go-server/internal/session/store_test.go`:

```go
// TestStoreListRenameDeleteTitle exercises ListSessions/RenameSession/DeleteSession
// and title persistence against a real Postgres.
func TestStoreListRenameDeleteTitle(t *testing.T) {
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
	store := NewPGStore(d.Pool())

	const tenantID = "00000000-0000-0000-0000-000000000001"
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		tenantID, "session-store-test"); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	const sessionID = "test-store-list"

	// Clean slate.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Build a session + one user message through the Manager so auto-title runs.
	mgr := NewManagerWithStore(store)
	s, err := mgr.GetOrCreate(ctx, sessionID, tenantID, "")
	if err != nil {
		t.Fatalf("get or create: %v", err)
	}
	s.Model = "qwen-3b"
	s.AddMessage(ctx, Message{Role: RoleUser, Content: "first message that is quite long and will be truncated"})

	// Auto-title: truncate to 40 runes + ellipsis.
	loaded, err := store.LoadSession(ctx, sessionID, tenantID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Title == "" {
		t.Fatalf("title empty, want auto-generated")
	}
	if runes := []rune(loaded.Title); len(runes) > 41 {
		t.Fatalf("title = %q, want ≤ 41 runes", loaded.Title)
	}

	// List returns the session with a title + 1 message.
	list, err := store.ListSessions(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("list empty, want ≥ 1")
	}
	var found *SessionSummary
	for i := range list {
		if list[i].ID == sessionID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("list = %+v, want contain %s", list, sessionID)
	}
	if found.MessageCount != 1 || found.Title == "" {
		t.Fatalf("found = %+v, want message_count=1 + non-empty title", *found)
	}

	// Rename.
	if err := store.RenameSession(ctx, sessionID, tenantID, "Renamed chat"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	loaded2, err := store.LoadSession(ctx, sessionID, tenantID)
	if err != nil {
		t.Fatalf("load after rename: %v", err)
	}
	if loaded2.Title != "Renamed chat" {
		t.Fatalf("title = %q, want Renamed chat", loaded2.Title)
	}

	// Cross-tenant rename must NOT find the session.
	if err := store.RenameSession(ctx, sessionID, "00000000-0000-0000-0000-000000000002", "x"); err != ErrSessionNotFound {
		t.Fatalf("cross-tenant rename err = %v, want ErrSessionNotFound", err)
	}

	// Delete.
	if err := store.DeleteSession(ctx, sessionID, tenantID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.LoadSession(ctx, sessionID, tenantID); err != ErrSessionNotFound {
		t.Fatalf("load after delete err = %v, want ErrSessionNotFound", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/session/ -run TestStoreListRenameDeleteTitle -v
```
Expected: FAIL — `undefined: store.ListSessions` / missing `Session.Title`.

- [ ] **Step 4: Add `Title` + auto-title to `session.go`**

Add `"strings"` to the import block. Add the `Title` field to the `Session` struct (after `SystemPrompt`):

```go
	// System prompt for this session.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Display title for the chat sidebar — auto-generated from the first user
	// message, or renamed by the user.
	Title string `json:"title,omitempty"`
```

In `AddMessage`, set the title **before** the `s.store` persistence block (right after `s.UpdatedAt = time.Now()`):

```go
	if s.Title == "" && msg.Role == RoleUser {
		s.Title = truncateTitle(msg.Content)
	}
```

Add the helper (bottom of `session.go`, near `EstimatedTokens`):

```go
// truncateTitle collapses whitespace and trims to 40 runes for the sidebar title.
func truncateTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return s
}
```

- [ ] **Step 5: Extend `store.go`**

Add `"time"` to the import block. Add `SessionSummary` type (after the error vars):

```go
// SessionSummary is a lightweight row for the chat sidebar list.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}
```

Add the three methods to the `Store` interface (inside `type Store interface { … }`):

```go
	// ListSessions returns sidebar summaries for the tenant, newest first.
	ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error)
	// RenameSession sets the display title.
	RenameSession(ctx context.Context, id, tenantID, title string) error
	// DeleteSession removes a session and its messages.
	DeleteSession(ctx context.Context, id, tenantID string) error
```

Update `UpsertSession` SQL to include `title` (column + value + conflict update):

```go
	_, err := p.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, model, system_prompt, max_tokens, title)
		VALUES ($1, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6::int, $7)
		ON CONFLICT (id) DO UPDATE SET
			model         = EXCLUDED.model,
			system_prompt = EXCLUDED.system_prompt,
			max_tokens    = EXCLUDED.max_tokens,
			title         = EXCLUDED.title,
			updated_at    = now()
		WHERE sessions.tenant_id = EXCLUDED.tenant_id`,
		s.ID, s.TenantID, s.UserID, s.Model, s.SystemPrompt, s.MaxTokens, s.Title)
```

Update `LoadSession` SELECT + Scan to read `title`:

```go
	err := p.pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, model, system_prompt, max_tokens, title, created_at, updated_at
		FROM sessions WHERE id = $1 AND tenant_id = $2`, id, tenantID).
		Scan(&s.TenantID, &userID, &s.Model, &s.SystemPrompt, &s.MaxTokens, &s.Title, &s.CreatedAt, &s.UpdatedAt)
```

Add the three method implementations (after `AppendMessage`):

```go
// ListSessions returns sidebar summaries for the tenant, newest first. The title
// falls back to the first user message so legacy rows (empty title) still show
// something meaningful.
func (p *PGStore) ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT s.id,
		       COALESCE(NULLIF(s.title, ''),
		                (SELECT m.content FROM messages m
		                 WHERE m.session_id = s.id AND m.role = 'user'
		                 ORDER BY m.seq ASC LIMIT 1),
		                '') AS title,
		       s.model, s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)::int AS message_count
		FROM sessions s
		WHERE s.tenant_id = $1::uuid
		ORDER BY s.updated_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSummary{}
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Model, &s.CreatedAt, &s.UpdatedAt, &s.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RenameSession sets the display title, scoped to the tenant.
func (p *PGStore) RenameSession(ctx context.Context, id, tenantID, title string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE sessions SET title = $3, updated_at = now()
		WHERE id = $1 AND tenant_id = $2::uuid`, id, tenantID, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteSession removes a session (messages cascade), scoped to the tenant.
func (p *PGStore) DeleteSession(ctx context.Context, id, tenantID string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND tenant_id = $2::uuid`, id, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}
```

- [ ] **Step 6: Add `Manager` wrappers in `manager.go`**

Append after `List()` (before `NewSessionID`):

```go
// ListSessions returns sidebar summaries for the tenant (store-backed).
func (m *Manager) ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	return m.store.ListSessions(ctx, tenantID)
}

// GetPersisted loads a session + messages from the store, scoped to the tenant.
// Returns ErrSessionNotFound when absent. The returned session is wired to the
// store so a later AddMessage persists rather than re-upserting.
func (m *Manager) GetPersisted(ctx context.Context, id, tenantID string) (*Session, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	s, err := m.store.LoadSession(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	s.store = m.store
	return s, nil
}

// RenameSession renames a session by id, scoped to the tenant.
func (m *Manager) RenameSession(ctx context.Context, id, tenantID, title string) error {
	if m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	return m.store.RenameSession(ctx, id, tenantID, title)
}

// DeleteSession removes a session from the store and the in-memory cache.
func (m *Manager) DeleteSession(ctx context.Context, id, tenantID string) error {
	if m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if err := m.store.DeleteSession(ctx, id, tenantID); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}
```

(`fmt` and `context` are already imported in `manager.go`.)

- [ ] **Step 7: Run the full session test suite**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/session/ -v
```
Expected: PASS (both `TestPGStoreRoundTrip` and `TestStoreListRenameDeleteTitle`).

- [ ] **Step 8: Commit**

```bash
git add go-server/internal/db/migrations/0005_session_title.sql go-server/internal/session/session.go go-server/internal/session/store.go go-server/internal/session/manager.go go-server/internal/session/store_test.go
git commit -m "feat(chat-history): session title + list/rename/delete in store"
```

---

### Task 3: Session HTTP endpoints (list / get / rename / delete)

**Files:**
- Modify: `go-server/internal/api/handler.go`
- Test: `go-server/internal/api/session_test.go`

**Interfaces:**
- Consumes (from Task 2): `Manager.ListSessions`, `Manager.GetPersisted`, `Manager.RenameSession`, `Manager.DeleteSession`, `session.ErrSessionNotFound`, `Session` fields (`ID`, `Title`, `Model`, `Messages`, `CreatedAt`, `UpdatedAt`).
- Consumes (existing): `auth.RequireAuth`, `auth.ClaimsFromContext` (returns `(*auth.Claims, bool)`; `claims.TenantID`, `claims.UserID`), `writeJSON`/`writeAPIError` (already in `api` package, `controlplane.go`).
- Produces (consumed by the frontend, Task 5):
  - `GET /api/v1/sessions` → `200` `[]session.SessionSummary`
  - `GET /api/v1/sessions/{id}` → `200` `{id, title, model, messages, created_at, updated_at}` | `404`
  - `PATCH /api/v1/sessions/{id}` body `{"title":"…"}` → `200` `{id, title}` | `404`
  - `DELETE /api/v1/sessions/{id}` → `204` | `404`

- [ ] **Step 1: Write the failing test**

`go-server/internal/api/session_test.go`:

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/google/uuid"
)

// TestSessionEndpointsE2E exercises the JWT-authed session CRUD endpoints.
func TestSessionEndpointsE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "sess-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	mgr := session.NewManagerWithStore(session.NewPGStore(d.Pool()))
	h := &Handler{sessionMgr: mgr, secret: secret, authSvc: authSvc, uiDir: t.TempDir()}
	cph := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	cph.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")
	authH := func() string { return "Bearer " + token }

	// Seed one session with a user message.
	sid := session.NewSessionID()
	sess, err := mgr.GetOrCreate(ctx, sid, tenant.ID, user.ID)
	if err != nil {
		t.Fatalf("get or create: %v", err)
	}
	sess.AddMessage(ctx, session.Message{Role: session.RoleUser, Content: "hello world"})

	// List contains the seeded session.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions", nil, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list []session.SessionSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	var found bool
	for _, s := range list {
		if s.ID == sid {
			found = true
		}
	}
	if !found {
		t.Fatalf("list = %+v, want contain %s", list, sid)
	}

	// Get returns messages.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("get code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages []session.Message `json:"messages"`
		Title    string            `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("get messages = %d, want 1", len(got.Messages))
	}

	// Rename.
	body, _ := json.Marshal(map[string]string{"title": "Renamed"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodPatch, "/api/v1/sessions/"+sid, body, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Get reflects the new title.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Title != "Renamed" {
		t.Fatalf("title = %q, want Renamed", got.Title)
	}

	// Delete → 204, then get → 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodDelete, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete code = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete code = %d, want 404", rec.Code)
	}

	// Missing auth → 401.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth list code = %d, want 401", rec.Code)
	}
}

func authedRequest(method, path string, body []byte, authHeader string) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", authHeader)
	return r
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/api/ -run TestSessionEndpointsE2E -v
```
Expected: FAIL — the old `/v1/sessions/` handler returns 401/404 (routes not yet auth'd, `PATCH` not handled).

- [ ] **Step 3: Replace `handleSessions` + routes in `handler.go`**

In `RegisterRoutes`, replace the line:
```go
	mux.HandleFunc("/v1/sessions/", h.handleSessions)
```
with:
```go
	mux.Handle("GET /api/v1/sessions", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleListSessions)))
	mux.Handle("GET /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleGetSession)))
	mux.Handle("PATCH /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleRenameSession)))
	mux.Handle("DELETE /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleDeleteSession)))
```

Delete the entire `handleSessions` function (lines 354–380) and replace it with:

```go
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing claims")
		return
	}
	list, err := h.sessionMgr.ListSessions(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	sess, err := h.sessionMgr.GetPersisted(r.Context(), id, claims.TenantID)
	if errors.Is(err, session.ErrSessionNotFound) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         sess.ID,
		"title":      sess.Title,
		"model":      sess.Model,
		"messages":   sess.Messages,
		"created_at": sess.CreatedAt,
		"updated_at": sess.UpdatedAt,
	})
}

func (h *Handler) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "title required")
		return
	}
	if err := h.sessionMgr.RenameSession(r.Context(), id, claims.TenantID, req.Title); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "title": req.Title})
}

func (h *Handler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if err := h.sessionMgr.DeleteSession(r.Context(), id, claims.TenantID); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

(All imports — `strings`, `errors`, `encoding/json`, `auth`, `session` — are already present in `handler.go`.)

- [ ] **Step 4: Run test to verify it passes**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/api/ -run TestSessionEndpointsE2E -v
```
Expected: PASS.

- [ ] **Step 5: Build the whole module**

```bash
cd go-server && go build ./...
```
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add go-server/internal/api/handler.go go-server/internal/api/session_test.go
git commit -m "feat(chat-history): session list/get/rename/delete HTTP endpoints"
```

---

### Task 4: Usage endpoint + record hook + wiring

**Files:**
- Modify: `go-server/internal/api/controlplane.go` — `handleGetUsage` + route + `strconv` import.
- Modify: `go-server/internal/api/handler.go` — `UsageRecorder` interface + `usage` field + record calls in `LoopEventFinal`.
- Modify: `go-server/cmd/server/main.go` — pass `cp` as `usage`.
- Modify: `go-server/internal/api/controlplane_test.go` — update the `NewHandler` call site.
- Test: `go-server/internal/api/usage_test.go`

**Interfaces:**
- Consumes (from Task 1): `Service.RecordUsage`, `Service.UsageSummary`, `Service.UsageDaily`, `Service.UsageByModel`, `UsageSummary`/`UsageDailyPoint`/`UsageByModel`.
- Consumes (existing): `auth.RequirePermission`, `auth.ActionUsageRead`, `auth.ClaimsFromContext`, `inference.Usage` (`PromptTokens int32`, `CompletionTokens int32`).
- Produces:
  - `type UsageRecorder interface { RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error }`
  - `GET /api/v1/usage?days=N` → `200` `{today, month, daily, by_model}` (each `today`/`month` a `controlplane.UsageSummary`; `daily` `[]UsageDailyPoint`; `by_model` `[]UsageByModel`).

- [ ] **Step 1: Write the failing test**

`go-server/internal/api/usage_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/google/uuid"
)

// TestUsageEndpointE2E exercises GET /api/v1/usage against a real Postgres.
func TestUsageEndpointE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "usage-e2e-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	if err := cp.RecordUsage(ctx, tenant.ID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := cp.RecordUsage(ctx, tenant.ID, "qwen-3b", 20, 10); err != nil {
		t.Fatalf("record: %v", err)
	}

	cph := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())
	mux := http.NewServeMux()
	cph.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Today   controlplane.UsageSummary `json:"today"`
		Month   controlplane.UsageSummary `json:"month"`
		Daily   []controlplane.UsageDailyPoint `json:"daily"`
		ByModel []controlplane.UsageByModel `json:"by_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Today.TotalTokens != 45 || resp.Today.Requests != 2 {
		t.Fatalf("today = %+v, want total=45 requests=2", resp.Today)
	}
	if resp.Month.TotalTokens != 45 {
		t.Fatalf("month total = %d, want 45", resp.Month.TotalTokens)
	}
	if len(resp.ByModel) != 1 || resp.ByModel[0].Model != "qwen-3b" {
		t.Fatalf("by_model = %+v, want [qwen-3b]", resp.ByModel)
	}

	// No auth → 401.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth code = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/api/ -run TestUsageEndpointE2E -v
```
Expected: FAIL — route `/api/v1/usage` not registered (`404`).

- [ ] **Step 3: Add `handleGetUsage` + route to `controlplane.go`**

Add `"strconv"` to the import block. Add the route in `RegisterRoutes` (after the quotas routes):

```go
	mux.Handle("GET /api/v1/usage", auth.RequirePermission(h.secret, auth.ActionUsageRead)(http.HandlerFunc(h.handleGetUsage)))
```

Add the handler (in the `--- quotas ---` section, after `handleListQuotas`):

```go
func (h *ControlPlaneHandler) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())

	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 90 {
			days = n
		}
	}

	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := now.AddDate(0, 0, -30)
	dailyStart := now.AddDate(0, 0, -(days - 1))

	today, err := h.cp.UsageSummary(r.Context(), claims.TenantID, todayStart, now)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	month, err := h.cp.UsageSummary(r.Context(), claims.TenantID, monthStart, now)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	daily, err := h.cp.UsageDaily(r.Context(), claims.TenantID, dailyStart, now)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	byModel, err := h.cp.UsageByModel(r.Context(), claims.TenantID, monthStart, now)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"today":    today,
		"month":    month,
		"daily":    daily,
		"by_model": byModel,
	})
}
```

(`time` is already imported in `controlplane.go`.)

- [ ] **Step 4: Add the record hook + `usage` field to `handler.go`**

Add the interface (after the `DeploymentResolver` interface):

```go
// UsageRecorder persists token usage per completed turn. Satisfied by *controlplane.Service.
type UsageRecorder interface {
	RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
}
```

Add the field to `Handler` (after `resolver DeploymentResolver`):

```go
	resolver   DeploymentResolver
	usage      UsageRecorder
```

Update `NewHandler` signature and body:

```go
func NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string, authSvc *auth.Service, secret []byte, resolver DeploymentResolver, usage UsageRecorder, limiter ratelimit.Limiter, rpmLimit, concLimit int) *Handler {
	return &Handler{
		sessionMgr: sessionMgr, loop: loop, uiDir: uiDir, authSvc: authSvc, secret: secret,
		resolver: resolver, usage: usage, limiter: limiter, rpmLimit: rpmLimit, concLimit: concLimit,
	}
}
```

In `handleOpenAIStream`, `case agent.LoopEventFinal` (line 241–245), replace:

```go
				case agent.LoopEventFinal:
					// Usage metering (A6): record prompt/completion tokens.
					if event.Usage != nil {
						observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
					}
```

with:

```go
				case agent.LoopEventFinal:
					// Usage metering: Prometheus counter + durable usage_events row.
					if event.Usage != nil {
						observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
						if h.usage != nil {
							if err := h.usage.RecordUsage(ctx, tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens)); err != nil {
								slog.Warn("record usage", "err", err)
							}
						}
					}
```

In `handleOpenAINonStream`, `case agent.LoopEventFinal` (line 296–301), replace:

```go
				case agent.LoopEventFinal:
					usage = event.Usage
					// Usage metering (A6): record prompt/completion tokens.
					if event.Usage != nil {
						observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
					}
```

with:

```go
				case agent.LoopEventFinal:
					usage = event.Usage
					// Usage metering: Prometheus counter + durable usage_events row.
					if event.Usage != nil {
						observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
						if h.usage != nil {
							if err := h.usage.RecordUsage(ctx, tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens)); err != nil {
								slog.Warn("record usage", "err", err)
							}
						}
					}
```

- [ ] **Step 5: Update the two `NewHandler` call sites**

`go-server/cmd/server/main.go` line 136:
```go
	handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret), cp, cp, limiter, cfg.RateLimitRPM, cfg.RateLimitConcurrency)
```

`go-server/internal/api/controlplane_test.go` line 490:
```go
	h := NewHandler(sess, loop, t.TempDir(), authSvc, secret, cp, cp, nil, 60, 4)
```

- [ ] **Step 6: Run the api tests + build**

```bash
AI_FACTORY_DATABASE_URL=postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable go test ./internal/api/ -run 'TestUsageEndpointE2E|TestSessionEndpointsE2E|TestInferenceAuthRequiredE2E' -v
cd go-server && go build ./...
```
Expected: all PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add go-server/internal/api/controlplane.go go-server/internal/api/handler.go go-server/internal/api/usage_test.go go-server/internal/api/controlplane_test.go go-server/cmd/server/main.go
git commit -m "feat(usage): GET /api/v1/usage + record hook wired into chat"
```

---

### Task 5: Chat history sidebar (frontend)

**Files:**
- Modify: `web/src/lib/types.ts` — add `SessionSummary`.
- Create: `web/src/components/SessionsSidebar.tsx`
- Modify: `web/src/components/ChatClient.tsx` — controlled `sessionId` + `onSessionChanged`.
- Create: `web/src/components/ChatPageClient.tsx`
- Modify: `web/src/app/(app)/chat/page.tsx`

**Interfaces:**
- Consumes: `apiFetch<T>` (from `web/src/lib/api.ts`), `useAuth()` (`token`), `Markdown` component, `Model` type.
- Produces (consumed by `/chat`): `SessionsSidebar` (props `sessions`, `activeId`, `onSelect`, `onNew`, `onChanged`), `ChatPageClient`, `ChatClient` (props `sessionId`, `onSessionChanged`).
- Backend contract: `GET /api/v1/sessions` → `SessionSummary[]`; `GET /api/v1/sessions/{id}` → `{title, model, messages:[{role, content, tool_result?, tool_calls?}], created_at, updated_at}`; `PATCH /api/v1/sessions/{id}` body `{title}`; `DELETE /api/v1/sessions/{id}`.

- [ ] **Step 1: Add the `SessionSummary` type**

Append to `web/src/lib/types.ts`:

```ts
export interface SessionSummary {
  id: string;
  title: string;
  model: string;
  created_at: string;
  updated_at: string;
  message_count: number;
}
```

- [ ] **Step 2: Create `SessionsSidebar.tsx`**

`web/src/components/SessionsSidebar.tsx`:

```tsx
"use client";

import { useState } from "react";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  const diff = Date.now() - then;
  const m = Math.floor(diff / 60000);
  if (m < 1) return "vừa xong";
  if (m < 60) return `${m} phút trước`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} giờ trước`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d} ngày trước`;
  return new Date(iso).toLocaleDateString("vi-VN");
}

interface Props {
  sessions: SessionSummary[];
  activeId: string;
  onSelect: (id: string) => void;
  onNew: () => void;
  onChanged: () => void;
}

export default function SessionsSidebar({ sessions, activeId, onSelect, onNew, onChanged }: Props) {
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  async function rename(id: string) {
    const title = draft.trim();
    if (!title) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "PATCH", body: { title } });
    setRenamingId(null);
    onChanged();
  }

  async function remove(id: string) {
    if (!window.confirm("Xoá hội thoại này?")) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "DELETE" });
    if (id === activeId) onNew();
    onChanged();
  }

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)]">
      <div className="border-b border-[var(--border)] p-3">
        <button
          onClick={onNew}
          className="w-full rounded-md bg-[var(--accent)] px-3 py-2 text-[13px] font-medium text-white hover:opacity-90"
        >
          ＋ Chat mới
        </button>
      </div>
      <div className="flex-1 overflow-y-auto py-1">
        {sessions.length === 0 && (
          <div className="px-4 py-6 text-center text-[12px] text-[var(--text2)]">Chưa có hội thoại nào.</div>
        )}
        {sessions.map((s) => (
          <div
            key={s.id}
            className={`group flex items-center gap-2 px-3 py-2 text-[13px] ${
              s.id === activeId ? "bg-[var(--surface2)] text-white" : "text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-white"
            }`}
          >
            {renamingId === s.id ? (
              <form
                className="flex-1"
                onSubmit={(e) => {
                  e.preventDefault();
                  rename(s.id);
                }}
              >
                <input
                  autoFocus
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  onBlur={() => rename(s.id)}
                  className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-1 py-0.5 text-[13px] outline-none"
                />
              </form>
            ) : (
              <button className="min-w-0 flex-1 text-left" onClick={() => onSelect(s.id)}>
                <div className="truncate">{s.title || "Hội thoại mới"}</div>
                <div className="text-[10px] text-[var(--text2)]">{relativeTime(s.updated_at)}</div>
              </button>
            )}
            <div className="hidden gap-1 group-hover:flex">
              <button
                className="text-[11px] text-[var(--text2)] hover:text-white"
                onClick={() => {
                  setRenamingId(s.id);
                  setDraft(s.title);
                }}
              >
                ✎
              </button>
              <button className="text-[11px] text-[var(--err)]" onClick={() => remove(s.id)}>
                ✕
              </button>
            </div>
          </div>
        ))}
      </div>
    </aside>
  );
}
```

- [ ] **Step 3: Refactor `ChatClient.tsx` to be controlled**

Make these edits to `web/src/components/ChatClient.tsx`:

**(a) Remove `SESSION_KEY`** (line 27) — keep `newSessionId` (still used for message IDs):
```tsx
const SESSION_KEY = "aif_session";

function newSessionId() {
```
→
```tsx
function newSessionId() {
```

**(b) Replace the component signature + `sessionId` state** (lines 34–57):
```tsx
export default function ChatClient() {
  const { token } = useAuth();
  const [sessionId, setSessionId] = useState<string>(() => {
    if (typeof window === "undefined") return "";
    return window.localStorage.getItem(SESSION_KEY) || newSessionId();
  });
  const [messages, setMessages] = useState<ChatMessage[]>([]);
```
→
```tsx
interface ChatClientProps {
  sessionId: string;
  onSessionChanged: () => void;
}

export default function ChatClient({ sessionId, onSessionChanged }: ChatClientProps) {
  const { token } = useAuth();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
```

**(c) Remove `sessionRef` + its sync effect** (lines 51–56):
```tsx
  const sessionRef = useRef(sessionId);

  // Keep the ref in sync so the async send() closure always reads the latest id.
  useEffect(() => {
    sessionRef.current = sessionId;
  }, [sessionId]);
```
→ delete (keep `abortRef` and `scrollRef`).

**(d) Key the restore effect on `sessionId`, reset messages, and use the new endpoint** (lines 58–86):
```tsx
  useEffect(() => {
    let cancelled = false;
    setMessages([]);
    apiFetch<{ title: string; model: string; messages: Array<{ role: string; content: string; tool_result?: string; tool_calls?: Array<{ name: string; arguments: string }>; is_error?: boolean }> }>(
      `/api/v1/sessions/${sessionId}`,
    )
      .then((data) => {
        if (cancelled) return;
        const mapped: ChatMessage[] = (data.messages || []).map((m) => {
          if (m.role === "tool") {
            return {
              id: newSessionId(),
              role: "tool",
              content: `🔧 Tool result: ${m.tool_result || "(empty)"}`,
            };
          }
          return { id: newSessionId(), role: m.role as ChatMessage["role"], content: m.content };
        });
        if (mapped.length) setMessages(mapped);
      })
      .catch(() => {
        // 404 = chưa có session này — giữ chat trống.
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId]);
```

**(e) In `send()`, replace `sessionRef.current` with `sessionId`** (line 155):
```tsx
          "x-session-id": sessionRef.current,
```
→
```tsx
          "x-session-id": sessionId,
```

**(f) Refresh the sidebar when a turn finishes** (in the `finally` block, line 245–248) and update the deps array (line 249):
```tsx
    } finally {
      if (abortRef.current === ac) abortRef.current = null;
      setBusy(false);
      onSessionChanged();
    }
  }, [input, busy, model, systemPrompt, token, sessionId, onSessionChanged]);
```

**(g) Remove `newConversation`** (lines 255–268) and the localStorage persistence effect (lines 270–273):
```tsx
  async function newConversation() { … }

  // Persist the session id across reloads so history is restored.
  useEffect(() => {
    window.localStorage.setItem(SESSION_KEY, sessionId);
  }, [sessionId]);
```
→ delete both.

**(h) Remove the "＋ Cuộc hội thoại mới" button** from the toolbar (lines 302–308):
```tsx
        <button
          onClick={newConversation}
          disabled={busy}
          className="rounded-md border border-[var(--border)] px-3 py-1 text-[12px] text-[var(--text2)] hover:bg-[var(--surface2)] disabled:opacity-50"
        >
          + Cuộc hội thoại mới
        </button>
```
→ delete.

Note: `useRef` is still imported and used (`abortRef`, `scrollRef`), so leave the import line unchanged.

- [ ] **Step 4: Create `ChatPageClient.tsx`**

`web/src/components/ChatPageClient.tsx`:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import ChatClient from "@/components/ChatClient";
import SessionsSidebar from "@/components/SessionsSidebar";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

function newSessionId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export default function ChatPageClient() {
  const [activeId, setActiveId] = useState<string>(() => newSessionId());
  const [sessions, setSessions] = useState<SessionSummary[]>([]);

  const refresh = useCallback(async () => {
    try {
      setSessions(await apiFetch<SessionSummary[]>("/api/v1/sessions"));
    } catch {
      /* server down — keep the current list */
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    refresh();
  }, [refresh]);

  return (
    <div className="flex h-full bg-[var(--bg)]">
      <SessionsSidebar
        sessions={sessions}
        activeId={activeId}
        onSelect={setActiveId}
        onNew={() => setActiveId(newSessionId())}
        onChanged={refresh}
      />
      <div className="min-w-0 flex-1">
        <ChatClient sessionId={activeId} onSessionChanged={refresh} />
      </div>
    </div>
  );
}
```

- [ ] **Step 5: Render `ChatPageClient` from the chat page**

Replace `web/src/app/(app)/chat/page.tsx` entirely:

```tsx
import ChatPageClient from "@/components/ChatPageClient";

export const metadata = { title: "Chat · AI Factory" };

export default function ChatPage() {
  return <ChatPageClient />;
}
```

- [ ] **Step 6: Verify build + lint**

```bash
cd web && npm run build
```
Expected: build succeeds with no type errors. (If `next build` is slow, `npx tsc --noEmit` is a fast type-check gate.)

- [ ] **Step 7: Smoke test (manual)**

With Go server + worker + web running, sign in → `/chat`: the sidebar shows sessions; "＋ Chat mới" starts a fresh chat; sending a first message adds a titled entry; the pencil icon renames; the ✕ icon deletes (with confirm); clicking a session restores its history.

- [ ] **Step 8: Commit**

```bash
git add web/src/lib/types.ts web/src/components/SessionsSidebar.tsx web/src/components/ChatClient.tsx web/src/components/ChatPageClient.tsx "web/src/app/(app)/chat/page.tsx"
git commit -m "feat(ui): ChatGPT-style chat history sidebar"
```

---

### Task 6: Platform console restructure (Usage + API Keys) + Infra page

**Files:**
- Create (git mv): `web/src/app/(app)/infra/page.tsx` ← from `web/src/app/(app)/platform/page.tsx` (rename h1/subtitle only).
- Rewrite: `web/src/app/(app)/platform/page.tsx` — Usage + API Keys tabs.
- Create: `web/src/components/ApiKeysTab.tsx` — moved from `keys/page.tsx`.
- Delete: `web/src/app/(app)/keys/page.tsx`.
- Modify: `web/src/lib/types.ts` — add usage types.
- Modify: `web/src/components/Sidebar.tsx` — nav: Chat / Platform / Infra.

**Interfaces:**
- Consumes: `apiFetch`, `DataTable`/`Column`/`Time`/`ShortId` (existing), `StatusBadge`, `APIKey` type.
- Backend contract: `GET /api/v1/usage` → `{today, month, daily, by_model}` (shapes from Task 4); `GET/POST/DELETE /api/v1/api-keys` (existing).

- [ ] **Step 1: Add usage types to `lib/types.ts`**

Append to `web/src/lib/types.ts`:

```ts
export interface UsageSummary {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  requests: number;
}

export interface UsageDailyPoint {
  date: string;
  prompt_tokens: number;
  completion_tokens: number;
  requests: number;
}

export interface UsageByModel {
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  requests: number;
}

export interface UsageResponse {
  today: UsageSummary;
  month: UsageSummary;
  daily: UsageDailyPoint[];
  by_model: UsageByModel[];
}
```

- [ ] **Step 2: Move the old platform page to `/infra`**

```bash
cd web/src/app/\(app\) && git mv platform/page.tsx infra/page.tsx
```
Then edit `infra/page.tsx`: change the heading and subtitle:
```tsx
      <h1 className="mb-1 text-lg font-semibold">Platform</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Quản lý model, serving template, deployment và quota cho tenant.
      </p>
```
→
```tsx
      <h1 className="mb-1 text-lg font-semibold">Infrastructure</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Quản lý model, serving template, deployment và quota cho tenant.
      </p>
```
No other change (all imports are `@/` aliases, unaffected by the move).

- [ ] **Step 3: Create `ApiKeysTab.tsx`**

Move the body of `keys/page.tsx` into a tab component. `web/src/components/ApiKeysTab.tsx`:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import DataTable, { Column, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { apiFetch } from "@/lib/api";
import type { APIKey } from "@/lib/types";

export default function ApiKeysTab() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setKeys(await apiFetch<APIKey[]>("/api/v1/api-keys"));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Không tải được danh sách key");
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function createKey(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<{ id: string; name: string; key: string }>("/api/v1/api-keys", {
        method: "POST",
        body: { name: name.trim() },
      });
      setNotice(`Đã tạo key "${created.name}". Chỉ hiển thị một lần: sk-…${created.key.slice(-8)}`);
      setName("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Tạo key thất bại");
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: string) {
    if (!window.confirm("Thu hồi API key này?")) return;
    try {
      await apiFetch(`/api/v1/api-keys/${id}`, { method: "DELETE" });
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Thu hồi thất bại");
    }
  }

  const columns: Column<APIKey>[] = [
    { key: "name", label: "Tên", render: (k) => <span className="font-medium">{k.name}</span> },
    { key: "id", label: "ID", render: (k) => <code className="text-[12px] text-[var(--link)]">{k.id.slice(0, 8)}…</code> },
    { key: "status", label: "Trạng thái", render: (k) => <StatusBadge status={k.status} /> },
    { key: "expires_at", label: "Hết hạn", render: (k) => (k.expires_at ? <Time iso={k.expires_at} /> : <span className="text-[var(--text2)]">Không</span>) },
    { key: "created_at", label: "Tạo lúc", render: (k) => <Time iso={k.created_at} /> },
  ];

  return (
    <div>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Key dùng cho `/v1/chat/completions` (Authorization: Bearer sk-…).
      </p>

      <form onSubmit={createKey} className="mb-6 flex items-center gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Tên key (vd: prod-bot)"
          className="flex-1 rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[14px] outline-none focus:border-[var(--accent)]"
        />
        <button
          type="submit"
          disabled={busy || !name.trim()}
          className="rounded-md bg-[var(--accent)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
        >
          {busy ? "Đang tạo…" : "+ Tạo key"}
        </button>
      </form>

      {notice && (
        <div className="mb-4 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-[13px] text-[var(--ok)]">
          {notice}
        </div>
      )}
      {error && (
        <div className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
          {error}
        </div>
      )}

      <DataTable
        columns={columns}
        rows={keys}
        empty="Chưa có API key nào."
        actions={(k) => (
          <button onClick={() => revoke(k.id)} className="text-[12px] text-[var(--err)] hover:underline">
            Thu hồi
          </button>
        )}
      />
    </div>
  );
}
```

Delete the old route file:
```bash
cd web/src/app/\(app\) && git rm keys/page.tsx
```

- [ ] **Step 4: Rewrite `platform/page.tsx` as the user console**

`web/src/app/(app)/platform/page.tsx`:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import ApiKeysTab from "@/components/ApiKeysTab";
import DataTable, { Column } from "@/components/DataTable";
import { apiFetch } from "@/lib/api";
import type { UsageByModel, UsageResponse } from "@/lib/types";

type Tab = "usage" | "keys";

const TABS: { id: Tab; label: string }[] = [
  { id: "usage", label: "Usage" },
  { id: "keys", label: "API Keys" },
];

function fmt(n: number): string {
  return n.toLocaleString("vi-VN");
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
      <div className="text-[12px] text-[var(--text2)]">{label}</div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
    </div>
  );
}

function UsageTab() {
  const [data, setData] = useState<UsageResponse | null>(null);
  const [days, setDays] = useState(30);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await apiFetch<UsageResponse>(`/api/v1/usage?days=${days}`));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Không tải được usage");
    }
  }, [days]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on days
    load();
  }, [load]);

  if (!data) {
    return <div className="py-10 text-center text-[13px] text-[var(--text2)]">{error || "Đang tải…"}</div>;
  }

  const maxTokens = Math.max(1, ...data.daily.map((d) => d.prompt_tokens + d.completion_tokens));

  const modelColumns: Column<UsageByModel>[] = [
    { key: "model", label: "Model", render: (m) => <code className="text-[12px] text-[var(--link)]">{m.model}</code> },
    { key: "requests", label: "Request", render: (m) => fmt(m.requests) },
    { key: "prompt_tokens", label: "Prompt tokens", render: (m) => fmt(m.prompt_tokens) },
    { key: "completion_tokens", label: "Completion tokens", render: (m) => fmt(m.completion_tokens) },
    { key: "total_tokens", label: "Tổng tokens", render: (m) => <span className="font-medium">{fmt(m.total_tokens)}</span> },
  ];

  return (
    <div>
      <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatCard label="Tokens hôm nay" value={fmt(data.today.total_tokens)} />
        <StatCard label="Requests hôm nay" value={fmt(data.today.requests)} />
        <StatCard label="Tokens 30 ngày" value={fmt(data.month.total_tokens)} />
        <StatCard label="Requests 30 ngày" value={fmt(data.month.requests)} />
      </div>

      <div className="mb-6 grid grid-cols-2 gap-3">
        <StatCard label="Prompt tokens (30 ngày)" value={fmt(data.month.prompt_tokens)} />
        <StatCard label="Completion tokens (30 ngày)" value={fmt(data.month.completion_tokens)} />
      </div>

      <div className="mb-2 flex items-center gap-2">
        <span className="text-[13px] text-[var(--text2)]">Biểu đồ token theo ngày</span>
        <div className="ml-auto flex gap-1">
          {[7, 30].map((n) => (
            <button
              key={n}
              onClick={() => setDays(n)}
              className={`rounded px-2 py-1 text-[12px] ${
                days === n ? "bg-[var(--surface2)] text-white" : "text-[var(--text2)] hover:text-white"
              }`}
            >
              {n} ngày
            </button>
          ))}
        </div>
      </div>
      <div className="mb-6 flex h-40 items-end gap-1 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-3">
        {data.daily.length === 0 && (
          <div className="w-full text-center text-[12px] text-[var(--text2)]">Chưa có usage trong khoảng này.</div>
        )}
        {data.daily.map((d) => {
          const total = d.prompt_tokens + d.completion_tokens;
          return (
            <div
              key={d.date}
              title={`${d.date}: ${fmt(total)} tokens`}
              className="min-w-[6px] flex-1 rounded-t bg-[var(--accent)]"
              style={{ height: `${Math.max(2, (total / maxTokens) * 100)}%` }}
            />
          );
        })}
      </div>

      <div className="mb-3 text-[13px] font-medium">Theo model (30 ngày)</div>
      <DataTable columns={modelColumns} rows={data.by_model} empty="Chưa có usage theo model." />
    </div>
  );
}

export default function PlatformPage() {
  const [tab, setTab] = useState<Tab>("usage");

  return (
    <div className="mx-auto max-w-5xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Platform</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Thống kê usage và quản lý API key cho tài khoản của bạn.
      </p>

      <div className="mb-6 flex gap-1 border-b border-[var(--border)]">
        {TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`rounded-t-md px-4 py-2 text-[13px] ${
              tab === t.id ? "border-b-2 border-[var(--accent)] text-white" : "text-[var(--text2)] hover:text-white"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "usage" && <UsageTab />}
      {tab === "keys" && <ApiKeysTab />}
    </div>
  );
}
```

- [ ] **Step 5: Update the nav sidebar**

Edit `web/src/components/Sidebar.tsx`, replace the `items` array (lines 8–12):
```tsx
const items = [
  { href: "/chat", label: "Chat", icon: "💬" },
  { href: "/keys", label: "API Keys", icon: "🔑" },
  { href: "/platform", label: "Platform", icon: "⚙️" },
];
```
→
```tsx
const items = [
  { href: "/chat", label: "Chat", icon: "💬" },
  { href: "/platform", label: "Platform", icon: "📊" },
  { href: "/infra", label: "Infra", icon: "⚙️" },
];
```

- [ ] **Step 6: Verify build**

```bash
cd web && npm run build
```
Expected: build succeeds. `npx tsc --noEmit` passes (no dangling import of the deleted `/keys` route or old `/platform` infra content).

- [ ] **Step 7: Smoke test (manual)**

Sign in → `/platform`: Usage tab shows cards + bar chart + per-model table (after at least one chat turn has recorded usage); API Keys tab creates/lists/revokes keys. `/infra` shows Deployments/Models/Templates/Quotas. Nav has Chat / Platform / Infra (Admin still shows for platform admins).

- [ ] **Step 8: Commit**

```bash
git add "web/src/app/(app)/infra/page.tsx" "web/src/app/(app)/platform/page.tsx" "web/src/app/(app)/keys/page.tsx" web/src/components/ApiKeysTab.tsx web/src/components/Sidebar.tsx web/src/lib/types.ts
git commit -m "feat(ui): platform console (usage + api keys) + infra page"
```

---

### Task 7: Documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/TRACKING.md`

- [ ] **Step 1: Update `CLAUDE.md`**

In the "Architecture" diagram, extend the Go server line to mention the new endpoints. In "Known Gaps", remove/adjust any gap now closed (chat history is now persisted with list/rename/delete; usage is persisted to `usage_events`). Add a bullet under "Key Decisions" noting: chat history (list/title/rename/delete) + per-turn usage are persisted to Postgres, and the UI console is `/platform` (Usage + API Keys) with infra at `/infra`.

Concretely, add these bullet points to the **Key Decisions** section:

```markdown
- **Chat history (sidebar):** sessions are durable (list/title/rename/delete via `/api/v1/sessions`), auto-titled from the first user message (40-rune truncate). The UI sidebar is ChatGPT-style on `/chat`.
- **Usage metering:** per-turn prompt/completion tokens are persisted to `usage_events` (best-effort, never fails a turn) and surfaced on `/platform` (Usage tab) + `/api/v1/usage`. Infra management (deployments/models/templates/quotas) moved to `/infra`.
```

And update the "Known Gaps" section: change the "Not yet: … observability (usage/tracing/cost)" line to reflect that usage persistence is now done (tracing/cost still open):

```markdown
- **Not yet:** persistence for other domains, sandbox for `run_command`, cost/quotas enforcement (usage is recorded but not yet enforced against quotas).
```

- [ ] **Step 2: Update `docs/TRACKING.md`**

Add a completed entry for this feature (chat history + usage + platform console) with the date `2026-08-16` and a pointer to the spec + this plan. Match the existing tracker's format (check the file's current "Done" vs "In progress" sections before editing).

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/TRACKING.md
git commit -m "docs: update CLAUDE.md + TRACKING for chat-history/usage/platform"
```

---

## Self-Review

**Spec coverage** — every spec section maps to a task:
- Migrations (`sessions.title`, `usage_events`) → Task 1, Task 2.
- `controlplane/usage.go` (RecordUsage + summaries) → Task 1.
- Session store extension (title, list/rename/delete) → Task 2.
- Session HTTP endpoints → Task 3.
- Usage endpoint + record hook + `NewHandler` wiring → Task 4.
- Chat sidebar (SessionsSidebar + controlled ChatClient) → Task 5.
- Platform console (Usage + API Keys) + infra rename + nav → Task 6.
- Docs → Task 7.

**Placeholder scan** — no TBD/TODO; every code step carries full code.

**Type consistency** — `UsageRecorder.RecordUsage` (Task 4 interface) matches `Service.RecordUsage` (Task 1). `Manager.ListSessions/GetPersisted/RenameSession/DeleteSession` (Task 2) match the handler calls (Task 3). `SessionSummary` JSON field names (`id`, `title`, `model`, `created_at`, `updated_at`, `message_count`) match the frontend type (Task 5). `UsageResponse` field names (`today`, `month`, `daily`, `by_model`) match the handler's `writeJSON` map (Task 4) and the frontend (Task 6). Both `NewHandler` call sites (`main.go`, `controlplane_test.go`) updated (Task 4).
