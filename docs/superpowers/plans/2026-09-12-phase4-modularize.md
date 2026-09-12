# Phase 4: Modularize (`services/*` split + infra relocation) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Break the flat `internal/*` layout into the production blueprint's **modular monolith** shape: split the god-package `controlplane` into `services/{iam,serving,usage}`, move `api`+`agent`+`session` into `services/inference`, and relocate the generic infrastructure packages under `internal/infrastructure/*` — **without changing a single HTTP route, JSON shape, status code, SSE frame, or DB column**.

**Architecture:** Phase 4 of `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` (§4.1, §4.2, §4.3, §8). The composition root `internal/app` becomes the *only* place that knows every service; services never import each other directly — cross-service seams are consumer-defined interfaces wired in `app`. The physical `cmd/{worker,migrate,seed}` split stays **Phase 5**; outbox/cache-aside/aggregate flush stays **Phase 6**.

**Tech Stack:** Go 1.25.7. No new third-party dependency — this is a pure move/refactor phase.

---

## Decisions (locked before Phase 4)

- **D-P4-1 — Scope: module split + infra relocation, flat file layout inside each service.** Create `internal/services/{iam,serving,usage,inference}` and `internal/infrastructure/{observability,circuitbreaker,retry,message,cache,inference,middleware}`. Inside each service files stay **flat** (e.g. `iam/handlers.go`, `iam/service.go`, `iam/repositories.go`) — the `handlers/ services/ repositories/ models/ dto/` subpackage split (§4.2) is **deferred to a later phase**. Rationale: the subpackage split roughly triples the churn and adds no behaviour; land the module boundary first, refine layers later.
- **D-P4-2 — Cross-service dependencies are consumer-defined interfaces wired in `app`.** `services/inference` declares `DeploymentResolver`, `UsageRecorder`, `Limiter`, `Auth`; `services/serving`/`services/usage` declare their own `Auth`. Concrete implementations (`serving.Service`, `usage.Service`, `iam.HTTPAuth`, `cache.RedisLimiter`) are injected by the composition root. **No `services/X` imports `services/Y`.**
- **D-P4-3 — Auth becomes a neutral port.** `internal/infrastructure/middleware` owns `Principal`, the `Authenticator` interface, the per-route middleware (`RequireAuth`, `RequirePermission`, `InferenceAuth`), the RBAC action constants, and `PrincipalFromContext`. `services/iam` *implements* `Authenticator` (JWT parse + API-key verify + `RoleAllows`) and owns JWT/password/RBAC logic. Routers import `infrastructure/middleware` only — never `services/iam`.
- **D-P4-4 — `infrastructure/inference` stops importing `session`.** The gRPC client owns its wire types (`inference.Message`, `inference.ToolCall`, `inference.ToolDefinition`); `services/inference`'s agent loop maps `session.Message → inference.Message`. Required to satisfy §4.2 ("infrastructure không gọi ngược lên services"). Behaviour-neutral.
- **D-P4-5 — One `ErrNotFound` per service.** `iam.ErrNotFound`, `serving.ErrNotFound`, `usage.ErrNotFound` are separate sentinels (no shared kernel). `serving.ErrInvalidTransition` likewise. Tests update `errors.Is` targets accordingly.
- **D-P4-6 — Shared JSON helpers move to `pkg/response`.** `WriteJSON` / `WriteAPIError` / `WriteOpenAIError` (gin-based), per spec §4.1.
- **D-P4-7 — Contract freeze.** Every route path, method, auth requirement, status code and JSON body must be byte-identical after the move. `docs/ARCHITECTURE.md` contract tests (intent) pass unmodified.
- **D-P4-8 — `app/seeder.go` stays in `app`.** Seeding spans iam (tenant/user) + serving (model/deployment); the composition root is the sanctioned place to cross-wire.
- **D-P4-9 — Direct move, no strangler shim.** Existing `controlplane`/`api`/`agent`/`session`/`auth` packages are deleted in the same phase (matches D-P3-1). No temporary aliasing packages.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- **No HTTP/behaviour change**: same paths, methods, status codes, JSON envelopes (`{"error":{"type","message","code"}}` + `{"error":{"code","message"}}`), SSE frames (`data:` per token, `[DONE]`, error frame).
- **No DB change**: no migration edits, no `AutoMigrate`, table/column names untouched.
- Global middleware order unchanged: **CORS → trace → logging → metrics**; recovery stays outermost. Per-route auth attaches per route as today.
- `metricWriter` must keep implementing `http.Flusher` (SSE depends on it).
- Security: never log tokens/prompts; log calls stay on `slog`.
- TDD: failing test → implement → pass → `go vet ./...` → `go test ./...` → commit. Integration tests still `t.Skip` when `AI_FACTORY_DATABASE_URL` is unset.
- Commit style matches history: `refactor(<scope>): <what>`.

## Route → service ownership (contract map, must not change)

| Route | Method | Owner | Auth |
|---|---|---|---|
| `/v1/chat/completions` | POST | inference | `InferenceAuth` |
| `/health` | GET | inference | none |
| `/api/v1/sessions`, `/api/v1/sessions/:id` | GET/PATCH/DELETE | inference | `RequireAuth` |
| `/`, `/chat`, `/keys`, `/concepts` | GET | inference (static UI) | none |
| `/api/v1/auth/login` | POST | iam | none |
| `/api/v1/api-keys`, `/api/v1/api-keys/:id` | POST/GET/DELETE | iam | `RequirePermission` (`key.manage`) |
| `/api/v1/tenants` | GET/POST | iam | `tenant.read` / `tenant.manage` |
| `/api/v1/models`, `/models/:id`, `/models/:id/versions` | GET/POST | serving | `model.read` / `model.write` |
| `/api/v1/templates`, `/templates/:id`, `/templates/:id/versions` | GET/POST | serving | `template.read` / `template.write` |
| `/api/v1/deployments`, `/deployments/:id`, `/deployments/:id/revisions`, `/deployments/:id/:action` | GET/POST | serving | `deployment.read` / `deployment.write` |
| `/api/v1/quotas` | GET/POST | usage | `usage.read` / `quota.manage` |
| `/api/v1/usage` | GET | usage | `usage.read` |
| `/metrics` | GET | app (infra) | none |

## Target file map

```
go-server/internal/
├── app/                         # composition root only
│   ├── app.go                   # lifecycle: seed → worker → HTTP → shutdown
│   ├── registry.go              # DI providers (infra → services → routers → worker)
│   ├── seeder.go                # cross-service seed (iam + serving)
│   └── options.go
├── config/                      # unchanged
├── migrations/                  # unchanged
├── infrastructure/
│   ├── database/                # unchanged (from Phase 2)
│   ├── observability/           # ← internal/observability
│   ├── circuitbreaker/          # ← internal/circuitbreaker
│   ├── retry/                   # ← internal/retry
│   ├── message/                 # ← internal/events
│   ├── cache/                   # ← internal/ratelimit
│   ├── inference/               # ← internal/inference (client + batch_scheduler + pb/)
│   └── middleware/              # NEW: global chain + auth port + RBAC actions
├── services/
│   ├── iam/                     # ← internal/auth + controlplane users/tenants/keys
│   ├── serving/                 # ← controlplane catalog/deployment + internal/runtime
│   ├── usage/                   # ← controlplane usage/quota
│   └── inference/               # ← internal/agent + internal/session + internal/api
└── pkg/
    ├── di/                      # unchanged
    └── response/                # NEW: WriteJSON / WriteAPIError / WriteOpenAIError
```

## File move map (source → destination)

| Current | Destination |
|---|---|
| `internal/observability/*` | `internal/infrastructure/observability/*` |
| `internal/circuitbreaker/*` | `internal/infrastructure/circuitbreaker/*` |
| `internal/retry/*` | `internal/infrastructure/retry/*` |
| `internal/events/*` | `internal/infrastructure/message/*` (package `message`, type names unchanged) |
| `internal/ratelimit/*` | `internal/infrastructure/cache/*` (package `cache`) |
| `internal/inference/*` | `internal/infrastructure/inference/*` (package `inference`) |
| `internal/inference/pb/*` | `internal/infrastructure/inference/pb/*` |
| `internal/auth/jwt.go` `password.go` `apikey.go` `rbac.go` `service.go` | `internal/services/iam/*` |
| `internal/controlplane/{service,types,users,catalog}.go` (IAM slice) | `internal/services/iam/` |
| `internal/controlplane/{models,repositories,repository_iam}.go` (IAM slice) | `internal/services/iam/` |
| `internal/controlplane/{model,deployment,state}.go` | `internal/services/serving/` |
| `internal/controlplane/{repositories,repository_serving}.go` (serving slice) | `internal/services/serving/` |
| `internal/controlplane/{idempotency,repository_idempotency}.go` | `internal/services/serving/` |
| `internal/runtime/*` | `internal/services/serving/` |
| `internal/controlplane/{usage,quota}.go` | `internal/services/usage/` |
| `internal/controlplane/{repositories,repository_usage}.go` (usage slice) | `internal/services/usage/` |
| `internal/agent/*` | `internal/services/inference/` |
| `internal/session/*` | `internal/services/inference/` |
| `internal/api/handler.go` `adapters.go` `sse.go` | `internal/services/inference/` |
| `internal/api/controlplane.go` (login/keys/tenants) | `internal/services/iam/handlers.go` |
| `internal/api/controlplane.go` (models/templates/deployments) | `internal/services/serving/handlers.go` |
| `internal/api/controlplane.go` (quotas/usage) | `internal/services/usage/handlers.go` |
| `internal/api/respond.go` | `pkg/response/response.go` |
| `internal/db/` (already gone), `internal/api/`, `internal/agent/`, `internal/session/`, `internal/auth/`, `internal/controlplane/`, `internal/events/`, `internal/ratelimit/`, `internal/observability/`, `internal/circuitbreaker/`, `internal/retry/`, `internal/inference/`, `internal/runtime/` | **deleted** at end of phase |

---

### Task 1: Relocate observability / circuitbreaker / retry

**Files:** move 3 dirs; rewrite imports in every consumer (app, api, agent, inference, runtime, and their tests).

- [ ] **Step 1: Move directories** with `git mv`:
  ```powershell
  git mv internal/observability internal/infrastructure/observability
  git mv internal/circuitbreaker internal/infrastructure/circuitbreaker
  git mv internal/retry          internal/infrastructure/retry
  ```
- [ ] **Step 2: Rewrite import paths** `internal/{observability,circuitbreaker,retry}` → `internal/infrastructure/...` in all `.go` files (consumers: `internal/app`, `internal/api`, `internal/agent`, `internal/inference`, `internal/runtime`, plus `internal/app/app_test.go`, `internal/runtime/worker_test.go`). Package names unchanged.
- [ ] **Step 3:** `go build ./...` && `go vet ./...`.
- [ ] **Step 4:** `go test ./...` (skips integration without DSN).
- [ ] **Step 5: Commit** `refactor(infra): move observability/circuitbreaker/retry under internal/infrastructure`.

---

### Task 2: Relocate events → message, ratelimit → cache

**Files:** move 2 dirs; rename package `events`→`message`, `ratelimit`→`cache`; rewrite references.

- [ ] **Step 1:** `git mv internal/events internal/infrastructure/message` and `git mv internal/ratelimit internal/infrastructure/cache`.
- [ ] **Step 2:** Change `package events` → `package message` in the moved files; `package ratelimit` → `package cache`.
- [ ] **Step 3:** Update all call sites: `events.` → `message.`, `ratelimit.` → `cache.` (consumers: `internal/app/registry.go`, `internal/app/app.go`, `internal/api/controlplane.go`, `internal/api/handler.go`, `internal/runtime/worker.go`, tests `events_test.go`, `kafka_test.go`, `memory_test.go`, `redis_test.go`). Type/const names stay identical (`Producer`, `Consumer`, `Event`, `NewEvent`, `TypeDeploymentCreated`, `TopicDeploymentEvents`, `NewKafkaEventBus`, `NewMemoryEventBus`, `Limiter`, `NewRedisLimiter`).
- [ ] **Step 4:** Build + vet + test.
- [ ] **Step 5: Commit** `refactor(infra): move events→message and ratelimit→cache`.

---

### Task 3: Relocate inference client → infrastructure/inference (decouple from `session`)

**Files:** move dir; add `infrastructure/inference/types.go`; modify `client.go`, `batch_scheduler.go`, `internal/agent/loop.go`.

**Interfaces produced:**
```go
// internal/infrastructure/inference/types.go
type Message struct { Role, Content, ToolCallID, ToolResult string; IsError bool; ToolCalls []ToolCall }
type ToolCall struct { ID, Name, Arguments string }
type ToolDefinition struct { Name, Description, Parameters string }
```
`GenerateRequest.Messages []Message`, `GenerateRequest.Tools []ToolDefinition` (was `[]session.Message` / `[]session.ToolDefinition`).

- [ ] **Step 1:** `git mv internal/inference internal/infrastructure/inference`; rewrite import paths (`internal/inference/pb` → `internal/infrastructure/inference/pb`).
- [ ] **Step 2: Write the new wire types** in `infrastructure/inference/types.go` mirroring the parts of `session.Message`/`ToolDefinition` the client actually uses (fields above).
- [ ] **Step 3: Point the client at the wire types** — replace `session.Message`/`session.ToolDefinition` fields and remove the `internal/session` import in `client.go`; update `buildProtoRequest` in `batch_scheduler.go` (no import change needed there).
- [ ] **Step 4: Map in the agent** — in `internal/agent/loop.go` convert session→wire before `TrySubmit`:
  ```go
  func toInferenceMessages(msgs []session.Message) []inference.Message { /* loop + tool_calls */ }
  func toInferenceTools(defs []ToolDefinition) []inference.ToolDefinition { /* name/description/parameters */ }
  ```
  `agent` now imports `internal/infrastructure/inference` (allowed: service→infra).
- [ ] **Step 5:** Fix `internal/inference/batch_scheduler_test.go` (moved) if it builds `GenerateRequest` with session messages — switch to `inference.Message`.
- [ ] **Step 6:** Build + vet + test. Assert `infrastructure/inference` has **no** `internal/services` / `internal/session` import.
- [ ] **Step 7: Commit** `refactor(infra): relocate inference client; own its wire types`.

---

### Task 4: `pkg/response` + global middleware → `infrastructure/middleware`

**Files:**
- Create `pkg/response/response.go` (+ test)
- Create `internal/infrastructure/middleware/global.go` (+ test) — move `recoveryMiddleware`, `corsMiddleware`, `traceMiddleware`, `loggingMiddleware`, `metricsMiddleware`, `metricWriter` out of `internal/app/app.go` as exported `Recovery()`, `CORS()`, `Trace()`, `Logging()`, `Metrics()` returning `gin.HandlerFunc`.
- Create `internal/infrastructure/middleware/auth.go` — the auth port (D-P4-3).

**Interfaces produced:**
```go
// pkg/response
func WriteJSON(c *gin.Context, status int, v any)
func WriteAPIError(c *gin.Context, status int, code, msg string)
func WriteOpenAIError(c *gin.Context, status int, typ, msg string)

// infrastructure/middleware
type Principal struct { TenantID, UserID, Role string }
type Authenticator interface {
    ParseToken(token string) (Principal, error)
    AuthenticateAPIKey(ctx context.Context, rawKey string) (Principal, error)
    Allows(role, action string) bool
}
func RequireAuth(auth Authenticator) gin.HandlerFunc
func RequirePermission(auth Authenticator, action string) gin.HandlerFunc
func InferenceAuth(auth Authenticator) gin.HandlerFunc
func PrincipalFromContext(c *gin.Context) (Principal, bool)
const ActionTenantManage = "tenant.manage" /* ...all 11 action constants... */
```
- [ ] **Step 1: Write failing tests** — `pkg/response/response_test.go` (status + body shapes for the 3 envelopes); `middleware/auth_test.go` over `gin.New()` with a fake `Authenticator` (401 missing/invalid token, 403 permission denied, 200 + `PrincipalFromContext` round-trip for JWT and API-key paths); `middleware/global_test.go` moves `app_test.go`'s metrics + flusher assertions.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** the three response helpers (bodies identical to today's `respond.go`), the global chain (same order/behaviour, same `metricWriter` embedding `gin.ResponseWriter` + `SetRouteLabels`), and the auth port. `InferenceAuth` must reproduce today's exact branches: JWT first, then API key; `ErrKeyInactive` → 403, other → 401. `Authenticator` implementations return `middleware.ErrInvalidToken` / `middleware.ErrKeyInactive`; define those sentinels in the middleware package.
- [ ] **Step 4:** Point `internal/api/respond.go` at `pkg/response` (or leave `respond.go` as thin wrappers until Task 7 deletes it) and leave the existing `internal/auth` middleware in place for now. `internal/app/app.go` switches to `middleware.Recovery()/CORS()/...`.
- [ ] **Step 5:** Build + vet + test.
- [ ] **Step 6: Commit** `refactor(http): shared response helpers + neutral middleware/auth port`.

---

### Task 5: `services/iam`

**Files:**
- Create `internal/services/iam/`: `service.go`, `types.go`, `models.go`, `repositories.go`, `repository_iam.go` (moved IAM slice of controlplane), `auth.go` (was `auth/service.go`), `jwt.go`, `password.go`, `apikey.go`, `rbac.go` (moved from `auth`), `authenticator.go` (implements `middleware.Authenticator`), `handlers.go` (login/keys/tenants moved from `api/controlplane.go`), `router.go`.
- Move `internal/auth/*_test.go`, `internal/controlplane/users_test.go` here.
- **Modify** `internal/controlplane/*` to drop the IAM slice (service methods, types, rows, repos); update `internal/app/{registry,seeder}.go`; update `internal/api/controlplane.go` to stop serving login/keys/tenants.
- Delete `internal/auth/`.

**Interfaces produced:**
```go
type Repositories struct { Tenants TenantRepository; Users UserRepository; APIKeys APIKeyRepository }
func NewService(repos Repositories) *Service           // CreateTenant, ListTenants, CreateUser, GetUserByUsername, CreateAPIKey, GetAPIKeyByHash, ListAPIKeys, DeleteAPIKey
func NewServiceFromGorm(db *gorm.DB) *Service
func NewAuthService(store Store, secret []byte, ttl time.Duration) *AuthService  // Login, AuthenticateAPIKey
func (s *Service) NewHTTPAuth(auth *AuthService) middleware.Authenticator
func (h *Handler) RegisterRoutes(e *gin.Engine, auth middleware.Authenticator)
```
- [ ] **Step 1: Write failing tests** — move `users_test.go` (asserts on `NewService(Repositories{...})`) and the iam portion of `api/controlplane_test.go` (`TestLoginE2E`, api-key create/list/delete) to `services/iam/handlers_test.go`; assert login returns `access_token`, keys require `key.manage`.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** by moving the listed files; split `controlplane/models.go` and `repositories.go` so iam keeps only tenant/user/api-key rows + repo interfaces; split `controlplane/service.go` into `iam.Service` (IAM methods) and keep a temporary `controlplane.Service` holding the rest (serving/usage) — the temporary controlplane service keeps compiling until Tasks 6–7.
- [ ] **Step 4: Implement the `Authenticator`** (`authenticator.go`): `ParseToken` → iam JWT `ParseToken` mapped to `middleware.Principal`; `AuthenticateAPIKey` → `AuthService.AuthenticateAPIKey` → `Principal{TenantID: key.TenantID}`; `Allows` → `RoleAllows`. Map `ErrKeyInactive`/`ErrInvalidAPIKey` to the middleware sentinels.
- [ ] **Step 5: Move handlers + router** from `api/controlplane.go`; replace `auth.ClaimsFromContext` with `middleware.PrincipalFromContext`, `controlplane.ErrNotFound` with `iam.ErrNotFound`, `writeJSON/writeAPIError` with `response.*`.
- [ ] **Step 6:** Update `app/registry.go` to register `iam.service` + `iam.auth` + `iam.http` and mount `iam.http.RegisterRoutes(e, auth)`. Update `seeder.go` (`auth.HashPassword`→`iam.HashPassword`, `controlplane.ErrNotFound`→`iam.ErrNotFound`, `cp.CreateTenant/CreateUser/GetUserByUsername/ListTenants`→`iam`. Update `api/controlplane.go` to drop login/keys/tenants.
- [ ] **Step 7:** Build + vet + test.
- [ ] **Step 8: Commit** `refactor(iam): extract services/iam from auth + controlplane`.

---

### Task 6: `services/serving`

**Files:**
- Create `internal/services/serving/`: `service.go`, `model.go`, `models.go`, `deployment.go`, `state.go`, `repositories.go`, `repository_serving.go`, `idempotency.go`, `repository_idempotency.go` (moved serving slice + idempotency), plus runtime files `runtime.go`, `worker.go`, `worker_adapter.go`, `mock.go` (moved from `internal/runtime`), `handlers.go` (models/templates/deployments from `api/controlplane.go`), `router.go`.
- Move `internal/runtime/*_test.go` and `internal/controlplane/{catalog,deployment,models,state,idempotency}_test.go` here.
- Delete `internal/runtime/`.

**Interfaces produced:**
```go
type Repositories struct { Models ModelRepository; Templates TemplateRepository; Deployments DeploymentRepository; Idempotency IdempotencyRepository }
func NewService(repos Repositories) *Service
func NewServiceFromGorm(db *gorm.DB) *Service
type DeploymentStore interface { /* unchanged, used by Worker */ }
func (h *Handler) RegisterRoutes(e *gin.Engine, auth middleware.Authenticator, producer message.Producer)
```
- [ ] **Step 1: Write failing tests** — move serving tests; assert model/template/deployment CRUD + `POST /deployments` publishes `deployment_created` and returns 202; runtime worker state-machine tests unchanged.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** the move; split the remaining `controlplane` rows/repos so serving owns model/template/deployment/revision/endpoint + idempotency rows. `service.go` holds `CreateModel...CreateEndpoint`, `TransitionDeployment`, idempotency methods. `ErrNotFound`/`ErrInvalidTransition` become `serving.*`.
- [ ] **Step 4:** `runtime.Worker` imports `serving` (same package now) + `message` + `infrastructure/circuitbreaker` + `infrastructure/retry`. Keep behaviour identical.
- [ ] **Step 5: Move handlers + router** from `api/controlplane.go`; `auth.RequirePermission`→`middleware.RequirePermission`, `auth.ClaimsFromContext`→`middleware.PrincipalFromContext`.
- [ ] **Step 6:** Update `app/registry.go` (serving service/handler/worker) and `seeder.go` (serving methods + `serving.ErrNotFound`). `api/controlplane.go` now only has quotas/usage.
- [ ] **Step 7:** Build + vet + test.
- [ ] **Step 8: Commit** `refactor(serving): extract services/serving (catalog, deployment, runtime)`.

---

### Task 7: `services/usage` (and delete `internal/controlplane`)

**Files:**
- Create `internal/services/usage/`: `service.go`, `usage.go`, `quota.go`, `models.go`, `repositories.go`, `repository_usage.go`, `handlers.go` (quotas/usage), `router.go`.
- Move `internal/controlplane/{usage_test,quota_test}.go` + some of `internal/api/usage_test.go` here.
- **Delete** `internal/controlplane/` and `internal/api/controlplane.go`/`api/usage_test.go`(moved) entirely.

**Interfaces produced:**
```go
type Repositories struct { Quotas QuotaRepository; Usage UsageRepository }
func NewService(repos Repositories) *Service
func NewServiceFromGorm(db *gorm.DB) *Service
func (h *Handler) RegisterRoutes(e *gin.Engine, auth middleware.Authenticator)
```
- [ ] **Step 1: Write failing tests** — move quota/usage tests; assert `GET /api/v1/usage` shape `{today,month,daily,by_model}` and quota upsert/list.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** the move; `usage.ErrNotFound` sentinel even if unused today. `RecordUsage` stays exactly `RecordUsage(ctx, tenantID, model string, promptTokens, completionTokens int) error` so `*usage.Service` structurally satisfies `inference.UsageRecorder`.
- [ ] **Step 4:** Delete `internal/controlplane` (now empty) and update `app/registry.go` (usage service/handler) + `seeder.go` (no usage use) + remove all remaining `controlplane` import paths.
- [ ] **Step 5:** Build + vet + test.
- [ ] **Step 6: Commit** `refactor(usage): extract services/usage; remove controlplane god-package`.

---

### Task 8: `services/inference` (and delete `internal/api`, `agent`, `session`)

**Files:**
- Create `internal/services/inference/`: `loop.go`, `tools.go` (moved from `agent`); `session.go`, `manager.go`, `store.go`, `store_gorm.go`, `models.go` (moved from `session`); `handler.go`, `handlers_session.go`, `handlers_ui.go` (split from `api/handler.go`), `adapters.go`, `sse.go`, `router.go`.
- Move/rename tests: `internal/api/handler_test.go`, `session_test.go`, `internal/session/store_test.go`.
- **Delete** `internal/agent/`, `internal/session/`, `internal/api/`.

**Interfaces produced (consumer-defined, D-P4-2):**
```go
type ResolvedDeployment struct { ID, TenantID, Region string }
type DeploymentResolver interface { ResolveDeployment(ctx context.Context, tenantID, modelName string) (*ResolvedDeployment, error) }
type UsageRecorder interface { RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error }
type Limiter interface { Allow(...); Acquire(...); Release(...) }   // or reuse cache.Limiter
type Auth interface { InferenceAuth() gin.HandlerFunc; RequireAuth() gin.HandlerFunc; Principal(c *gin.Context) (tenantID, userID string, ok bool) }
type Deps struct { Sessions *Manager; Loop *Loop; Auth Auth; Resolver DeploymentResolver; Usage UsageRecorder; Limiter Limiter; RPM, Concurrency int; UIDir string }
func (h *Handler) RegisterRoutes(e *gin.Engine, d Deps)
```
- [ ] **Step 1: Write failing tests** — move `handler_test.go` (change `fakeResolver` to return `*ResolvedDeployment`; replace `controlplane.ErrNotFound` with a plain `errors.New`), `session_test.go`, `store_test.go`.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** the moves. `Agent.Loop` imports `infrastructure/inference` + `infrastructure/observability` + local session types. `Handler` holds `DeploymentResolver`/`UsageRecorder`/`Limiter` and the `Auth` port; replace `auth.ClaimsFromContext`→`auth.Principal`, `auth.InferenceAuth`→`d.Auth.InferenceAuth()`, `controlplane.Deployment`→`ResolvedDeployment`, `writeJSON/writeAPIError/writeOpenAIError`→`response.*`, `session.ErrSessionForbidden`/`ErrSessionNotFound` stay local.
- [ ] **Step 4:** Provide a small adapter in `app` that maps `serving.Service.ResolveDeployment` (`*serving.Deployment`) → `inference.ResolvedDeployment`; pass `usage.Service` directly as `UsageRecorder`; pass `iam.HTTPAuth` as `Auth`.
- [ ] **Step 5:** Delete `internal/api`, `internal/agent`, `internal/session`; update `app/registry.go` + `app.go` (four routers: iam, serving, usage, inference).
- [ ] **Step 6:** Build + vet + test.
- [ ] **Step 7: Commit** `refactor(inference): extract services/inference (chat, agent, sessions)`.

---

### Task 9: Composition root finalization + full verification

**Files:** `internal/app/app.go`, `internal/app/registry.go`, `internal/app/seeder.go`.

- [ ] **Step 1:** Confirm `RegisterAll` order: infrastructure (logger, db, redis, cache limiter, message bus, inference client, batch scheduler, tool executor) → services (iam, serving, usage, inference loop/sessions) → routers (iam/serving/usage/inference handlers) → worker (serving).
- [ ] **Step 2:** Confirm `NewAppFromContainer` force-resolve list uses the new names and `NewHTTPHandler` mounts all four `RegisterRoutes` + `/metrics` with the `middleware` global chain.
- [ ] **Step 3:** Confirm `Run()` seeds via `iam.Service` + `serving.Service` and starts the serving worker when Kafka is up.
- [ ] **Step 4:** `go build ./...`, `go vet ./...`, `go test ./...`.
- [ ] **Step 5: Boot smoke** (Postgres up): `/health` 200, `POST /api/v1/auth/login` → token, `GET /api/v1/models` 200, `POST /v1/chat/completions` non-stream + stream (`curl -N`) work, `/metrics` scrapes.
- [ ] **Step 6:** Grep guard: no import of `internal/{api,agent,session,auth,controlplane,events,ratelimit,observability,circuitbreaker,retry,inference,runtime}` remains; no `services/X` imports `services/Y`.
- [ ] **Step 7: Commit** `refactor(app): wire modular services; retire flat packages`.

---

### Task 10: Documentation

**Files:** `CLAUDE.md`, `docs/TRACKING.md`, `docs/ARCHITECTURE.md`, spec gap note.

- [ ] Mark Phase 4 ✅, update the project-structure tree (services + infrastructure), the architecture diagram, and the roadmap line (`Phase 4 ✅`). Update the update-log table in `TRACKING.md`.
- [ ] Note the deferred layer subpackages (`handlers/services/repositories/models/dto`) and the deferred `cmd/{worker,migrate,seed}` (Phase 5).

---

## Test migration map

| Old test | New location |
|---|---|
| `internal/observability/*_test.go` | `internal/infrastructure/observability/` |
| `internal/circuitbreaker/*_test.go`, `retry_test.go` | `internal/infrastructure/{circuitbreaker,retry}/` |
| `internal/events/*_test.go` | `internal/infrastructure/message/` |
| `internal/ratelimit/redis_test.go` | `internal/infrastructure/cache/` |
| `internal/inference/batch_scheduler_test.go` | `internal/infrastructure/inference/` |
| `internal/app/app_test.go` | `internal/app/` (update imports; metrics/flusher tests move to `middleware/global_test.go`) |
| `internal/auth/*_test.go`, `internal/controlplane/users_test.go` | `internal/services/iam/` |
| `internal/controlplane/{catalog,deployment,models,state,idempotency}_test.go`, `internal/runtime/*_test.go` | `internal/services/serving/` |
| `internal/controlplane/{usage,quota}_test.go`, `internal/api/usage_test.go` | `internal/services/usage/` |
| `internal/api/handler_test.go`, `session_test.go`, `internal/session/store_test.go` | `internal/services/inference/` |
| `internal/api/controlplane_test.go` (login/keys) | `internal/services/iam/handlers_test.go` |
| `internal/api/controlplane_test.go` (models/templates/deployments) | `internal/services/serving/handlers_test.go` |
| baseline/infra tests | unchanged |

## Self-Review Checkpoints

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` clean.
- [ ] Old flat packages gone: `api`, `agent`, `session`, `auth`, `controlplane`, `events`, `ratelimit`, `observability`, `circuitbreaker`, `retry`, `inference`, `runtime` (all under `internal/infrastructure/*` or `internal/services/*` now).
- [ ] No `services/X` imports `services/Y`; cross-service seams are interfaces declared by the consumer and wired in `app`.
- [ ] `infrastructure/inference` does not import any service (checked by grep).
- [ ] Every route/method/status/JSON/SSE frame byte-identical (contract map above).
- [ ] Global middleware order CORS → trace → logging → metrics preserved; `metricWriter` still implements `http.Flusher`.
- [ ] SSE streams token-by-token through the new inference router (boot + `curl -N` smoke).
- [ ] No migration/DB change; integration tests still skip without `AI_FACTORY_DATABASE_URL`.
- [ ] `go-server/internal/controlplane` deleted; no `pgx`/`goose`/`Auth`-flat imports remain.
- [ ] Seeder works E2E (admin + demo tenant + model + READY deployment), inference routes resolve a deployment.
