# UI usability fixes — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Fix the 5 blockers that stop the `web/` UI from being usable end-to-end: (1) API key is never shown in full, (2) no way to create a user, (3) deployment creation requires hand-copying UUIDs, (4) async deployment status does not refresh, (5) chat defaults to a model that may not exist. Adds the minimum Go endpoints required (list versions, create/list users).

**Scope:** frontend (`web/`) + minimal backend (`go-server/`) endpoints. No change to existing response/SSE shapes. No UX-polish items (401 auto-logout, URL session ids, message copy/regenerate, password prefill) — those stay deferred.

**Tech:** Next.js 16 (App Router, React 19, Tailwind v4) + Gin handlers on the existing modular services. No new dependency on either side.

---

## Decisions (locked before this pass)

- **D-UI-1 — API key shown once, client-side only.** `POST /api/v1/api-keys` already returns the full `key` (`iam/handlers.go:62`). The UI renders it in a one-time banner with a copy button; the value is never persisted and disappears on reload/next create.
- **D-UI-2 — List-versions endpoints are tenant-agnostic.** `GET /api/v1/models/:id/versions` and `GET /api/v1/templates/:id/versions` return all versions of the catalog object, guarded by `model.read` / `template.read` (same as the object's `GET`). Catalog is global by design today; no tenant scoping is introduced.
- **D-UI-3 — Deployment form uses cascading dropdowns.** Model select → its versions; template select → its versions. The free-text UUID inputs are removed. IDs in tables become copyable (`CopyButton`).
- **D-UI-4 — Poll only while non-terminal.** Deployments tab polls every 3s while any row is `PENDING`/`PROVISIONING`/`STARTING`/`STOPPING`; stops when all rows are terminal (`READY`/`STOPPED`/`FAILED`/`DEGRADED`) or the tab unmounts.
- **D-UI-5 — Users are created by a platform admin.** `POST /api/v1/users` is guarded by `tenant.manage` (PLATFORM_ADMIN only), takes `tenant_id` from the body. Self-service tenant-admin user management is a later item. New users get role `TENANT_ADMIN`/`TENANT_DEVELOPER`/`TENANT_VIEWER`; the first user of a tenant should be `TENANT_ADMIN`.
- **D-UI-6 — Chat defaults from the registry.** Replace the hardcoded `qwen3.5-9b` default with the first model returned by `/api/v1/models` (prefer `qwen-3b`). If the registry is empty or the send fails with 404, show a hint pointing at `/infra`.
- **D-UI-7 — A shared `CopyButton` component.** One small client component reused by API keys and infra tables; uses `navigator.clipboard` with a "Đã copy" transient state.

## Global Constraints

- Module `github.com/ai-factory/go-server` (Go 1.25.7); `web/` on Next.js 16 / TypeScript.
- No new third-party dependency.
- No existing HTTP route/JSON/SSE shape change.
- Security: never log raw API keys/passwords; never persist the revealed key; password is hashed with the existing `iam.HashPassword` before storage.
- TDD for backend changes: failing test → implement → pass.
- Verify: `go vet ./...`, `go test ./...` (Go), `npm run lint`, `npm run build` (web).
- Commit style: `feat(serving): ...` / `feat(iam): ...` / `feat(web): ...`.

---

### Task 1: API key — reveal full value + copy

**Files:** `web/src/components/CopyButton.tsx` (new), `web/src/components/ApiKeysTab.tsx`.

- [x] Create `CopyButton` (client): props `value`, optional `label`; `navigator.clipboard.writeText`, transient `copied` state ("Đã copy" for ~1.5s), fallback to a hidden textarea + `document.execCommand("copy")`.
- [x] In `ApiKeysTab`, keep the created key in state (`createdKey: string | null`) instead of discarding it.
- [x] Render a one-time banner/modal after create: full `sk-…` value in a monospace, selectable field + `<CopyButton value={createdKey} />` + a warning "Chỉ hiển thị một lần" + dismiss button.
- [x] Keep the existing list/revoke behavior unchanged.
- [x] `npm run lint` + `npm run build` clean.

### Task 2: Backend — list model/template versions

**Files:** `internal/services/serving/repositories.go`, `repository_serving.go`, `catalog.go`, `handlers.go`, `router.go` (+ tests).

- [x] Test first: a repository/service test listing versions of a model/template, newest first.
- [x] Add `ListVersions(ctx, modelID string) ([]ModelVersion, error)` to `ModelRepository` + `modelRepo` (`ORDER BY created_at`).
- [x] Add `ListVersions(ctx, templateID string) ([]TemplateVersion, error)` to `TemplateRepository` + `templateRepo`.
- [x] Add `Service.ListModelVersions` / `Service.ListTemplateVersions` wrappers.
- [x] Add `handleListModelVersions` / `handleListTemplateVersions` returning `200 []`.
- [x] Mount `GET /api/v1/models/:id/versions` (`model.read`) and `GET /api/v1/templates/:id/versions` (`template.read`) in `router.go`.
- [x] `go vet ./...`, `go test ./...` clean.

### Task 3: Infra — deployment form dropdowns + copyable IDs

**Files:** `web/src/app/(app)/infra/page.tsx`, `web/src/lib/types.ts` (reuse existing `ModelVersion`/`TemplateVersion`).

- [x] `DeploymentsTab`: load models and templates on mount; render a `model` `<select>`; on selection fetch `/api/v1/models/{id}/versions` and render a version `<select>`; same for templates. Submit the selected version IDs.
- [x] Disable the submit button until model/template/version are all chosen; show a hint when no models/templates exist (link to the Models/Templates tabs).
- [x] Use `CopyButton` in the `ModelVersion`/`TemplateVersion`/`ID` columns of the Models/Templates tables and in the deployment `ShortId` render (wrap, don't replace the truncated display).
- [x] `npm run lint` + `npm run build` clean.

### Task 4: Chat — model default from registry + empty-state hint

**Files:** `web/src/components/ChatClient.tsx`.

- [x] Change initial `models`/`model` state to empty; populate from `/api/v1/models`, preferring `qwen-3b`, else first name.
- [x] If no models load, keep the composer but show a hint "Chưa có model/deployment READY — vào /infra để tạo" instead of a generic send error.
- [x] On a `404 RESOURCE_NOT_FOUND` from the stream, surface a "Model chưa có deployment READY" message.
- [x] `npm run lint` + `npm run build` clean.

### Task 5: Infra — poll deployment status

**Files:** `web/src/app/(app)/infra/page.tsx` (`DeploymentsTab`).

- [x] Add an effect: while `rows.some(r => !TERMINAL.has(r.status))`, `setInterval(load, 3000)`; clear on cleanup and when all terminal.
- [x] `const TERMINAL = new Set(["READY","STOPPED","FAILED","DEGRADED"])`.
- [x] Ensure the interval does not stack (single timer per state transition).
- [x] `npm run lint` + `npm run build` clean.

### Task 6: Backend — create + list users

**Files:** `internal/services/iam/repositories.go`, `repository_iam.go`, `users.go`, `handlers.go`, `router.go` (+ tests).

- [x] Test first: create a user in a tenant → `GET` lists it with role/tenant; duplicate username fails cleanly.
- [x] Add `ListByTenant(ctx, tenantID string) ([]User, error)` to `UserRepository` + `userRepo` (join `users` + `tenant_memberships`, `ORDER BY created_at`).
- [x] Add `Service.ListUsers(ctx, tenantID)` and a `CreateUserWithPassword(ctx, username, email, password, role, tenantID)` that hashes via `HashPassword` and calls the existing `CreateUser`.
- [x] `handleCreateUser` (`POST /api/v1/users`): body `{username,email,password,role,tenant_id}`; validate non-empty; hash password; `201` with the `User`. `handleListUsers` (`GET /api/v1/users?tenant_id=`): `200 []`.
- [x] Mount both under `middleware.RequirePermission(h.auth, middleware.ActionTenantManage)`.
- [x] `go vet ./...`, `go test ./...` clean.

### Task 7: Admin — Users section

**Files:** `web/src/app/(app)/admin/page.tsx`, `web/src/lib/types.ts`.

- [x] Add a `User` type mirroring `iam.User` (`id, username, email, role, tenant_id, status`).
- [x] Add a "Users" section: tenant `<select>` (from `/api/v1/tenants`), username, email, password, role `<select>` (`TENANT_ADMIN`/`TENANT_DEVELOPER`/`TENANT_VIEWER`), submit `POST /api/v1/users`.
- [x] List users of the selected tenant via `GET /api/v1/users?tenant_id=`; show a note that the first user of a tenant should be `TENANT_ADMIN`.
- [x] Keep the platform-admin gate unchanged.
- [x] `npm run lint` + `npm run build` clean.

### Task 8: Docs + full verification

- [x] Run `go build ./...`, `go vet ./...`, `go test ./...` (Go) and `npm run lint`, `npm run build` (web) — all clean.
- [x] Manual smoke (server + worker + web up): create API key → copy full value; create model + version, template + version → deployment via dropdowns; watch status poll to READY; create tenant + user → log in as that user; send a chat turn.
- [x] Update `docs/TRACKING.md` (UI usability pass) and the `CLAUDE.md` UI bullet; note the deferred UX-polish items.

## Self-Review Checkpoints

- [x] A newly created API key can be copied in full and used against `/v1/chat/completions`.
- [x] A deployment can be created without typing any UUID by hand.
- [x] The deployments table reflects `PENDING→READY` without manual refresh.
- [x] Chat defaults to a routable model and explains when none is READY.
- [x] A new tenant can be given a `TENANT_ADMIN` user that can log in.
- [x] No existing route/JSON/SSE shape changed.
