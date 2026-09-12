# Phase 3: HTTP Layer (`net/http` → Gin) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Swap the HTTP layer from `net/http` + `http.ServeMux` to **Gin v1.11.0** — handlers, per-route auth middleware, the global middleware chain, and the `/metrics` mount — while keeping every route, response shape, status code, and the **SSE streaming contract byte-identical**.

**Architecture:** Phase 3 of `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` (§6.1, §8). `internal/app` builds a `*gin.Engine` instead of `*http.ServeMux`; `api.Handler` / `api.ControlPlaneHandler` expose `RegisterRoutes(*gin.Engine)`; `auth` middleware returns `gin.HandlerFunc`. The physical `services/*` split stays Phase 4.

**Tech Stack:** Go 1.25.7, `github.com/gin-gonic/gin` v1.11.0.

## Decisions (locked before Phase 3)

- **D-P3-1 — Direct swap, not strangler.** The app is small and all HTTP tests already go through `httptest` + a router; keeping both `net/http` and Gin in parallel buys nothing and doubles the test surface. Open question #3 in the spec → **direct swap**.
- **D-P3-2 — Auth middleware converts to Gin.** `auth.RequireAuth`, `auth.RequirePermission`, `auth.InferenceAuth` become `func(*gin.Context)` middleware that store claims/API key via `c.Set`/`c.Get`. Context accessors (`ClaimsFromContext`, `APIKeyFromContext`, `TenantIDFromContext`) are re-implemented to read from the Gin context but keep their existing call sites working by also accepting `context.Context`? **No** — handlers already have `c`, so accessors change to `*gin.Context`. Auth unit tests move to `gin.New()`.
- **D-P3-3 — Metrics labels via a wrapping `gin.ResponseWriter`.** A `metricWriter` embeds `gin.ResponseWriter`, captures the status in `WriteHeader`, and implements `observability.RouteLabelSetter`; the handler sets labels through `c.Writer.(RouteLabelSetter)` exactly like today's `statusRecorder`.
- **D-P3-4 — SSE keeps the existing `SSEWriter`.** The current `SSEWriter` (writes `data:` frames + flushes per token) is preserved and driven with `c.Writer` (a `gin.ResponseWriter`, which implements `http.Flusher`). We do **not** rewrite to `c.Stream` because: (a) behavior is already verified E2E including reasoning tokens, tool calls, `[DONE]`, and error frames; (b) `c.Stream` is a loop helper, not a behavior change. Revisit only if a Gin-specific buffering issue appears.
- **D-P3-5 — `/metrics` mounts via `gin.WrapH`.** `observability.MetricsHandler()` stays a `net/http` handler; Gin wraps it.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- **No response contract change**: same paths, methods, status codes, JSON envelope (`{"error":{"type","message","code"}}` and `{"error":{"code","message"}}`), SSE frames.
- Global middleware order unchanged: **CORS → trace → logging → metrics** (outermost → innermost).
- `metricWriter` must keep implementing `http.Flusher` (SSE depends on it — regression test moves with it).
- `gin.SetMode(gin.ReleaseMode)` at boot (no debug banner); Gin's default `Logger`/`Recovery` are disabled (`gin.New()`) — we keep our own logging + a recovery middleware.
- Security: never log tokens/prompts.
- TDD: failing test → implement → pass → `go vet ./...` → commit.

## File Structure

| File | Responsibility |
|---|---|
| `go-server/internal/api/gin_engine.go` | `NewEngine()` helper + `gin.WrapH` metrics mount helper (optional) |
| `go-server/internal/api/handler.go` | `*gin.Context` handlers, `RegisterRoutes(*gin.Engine)`, SSE via `c.Writer` |
| `go-server/internal/api/controlplane.go` | `*gin.Context` handlers + `RegisterRoutes(*gin.Engine)` (per-route `auth.RequirePermission`) |
| `go-server/internal/api/sse.go` | unchanged logic (accepts `http.ResponseWriter`) |
| `go-server/internal/api/respond.go` | `writeJSON`/`writeAPIError`/`writeOpenAIError` on `*gin.Context` |
| `go-server/internal/auth/middleware.go` | Gin middleware + `c.Set`/`c.Get` accessors |
| `go-server/internal/app/app.go` | `gin.Engine`, Gin global middleware chain, graceful shutdown, `/metrics` |
| `go-server/internal/api/*_test.go`, `internal/auth/middleware_test.go` | rewritten with `gin.New()` + `httptest` |

---

### Task 1: Gin dependency + engine + global middleware

**Files:**
- Modify: `go-server/go.mod`
- Modify: `go-server/internal/app/app.go`
- Modify: `go-server/internal/app/registry.go`
- Test: `go-server/internal/app/app_test.go` (metrics + flusher)

- [ ] **Step 1: Add dependency** — `cd go-server && go get github.com/gin-gonic/gin@v1.11.0`.

- [ ] **Step 2: Write failing tests** — keep `TestStatusRecorderImplementsFlusher` adapted to the new `metricWriter`; add `TestMetricsMiddlewareGin` that builds `gin.New()`, mounts the metrics middleware, runs a handler returning 418, and asserts the Prometheus counter increments and status passes through.

- [ ] **Step 3: Verify fail** (`undefined: metricWriter`).

- [ ] **Step 4: Implement Gin global middleware** in `app.go`:
  - `corsMiddleware(c *gin.Context)` — same headers incl. `x-session-id`, abort OPTIONS with 204.
  - `traceMiddleware(c *gin.Context)` — `observability.ExtractTraceparent(c.Request.Context(), c.GetHeader(...))`, `StartSpan`, `defer span.End()`, push ctx back onto `c.Request`.
  - `loggingMiddleware(c *gin.Context)` — `slog.Info("http request", "method", c.Request.Method, "path", c.Request.URL.Path)`.
  - `metricsMiddleware(c *gin.Context)` — wrap `c.Writer` in `metricWriter{ResponseWriter: c.Writer, status: 200}`, `c.Next()`, then `HTTPRequestsTotal`/`RequestDurationSeconds` with the captured labels.
  - `metricWriter` implements `SetRouteLabels` + delegates `Flush`.
  - `recoveryMiddleware(c *gin.Context)` — `defer func(){ if r := recover(); ... }` → 500 `internal_error`; replaces Gin's default `Recovery`.

- [ ] **Step 5: Implement `NewHTTPHandler`** returning `*gin.Engine`: `gin.SetMode(gin.ReleaseMode)`, `e := gin.New()`, `e.Use(corsMiddleware, traceMiddleware, loggingMiddleware, metricsMiddleware, recoveryMiddleware)`, then mount API + control-plane routes + `/metrics` via `gin.WrapH`.

- [ ] **Step 6: Pass + vet.**

- [ ] **Step 7: Commit** `feat(http): Gin engine + global middleware chain`.

---

### Task 2: Auth middleware → Gin

**Files:** `go-server/internal/auth/middleware.go`, `.../middleware_test.go`, `.../rbac.go` (unchanged).

- [ ] **Step 1: Write failing tests** — `middleware_test.go` runs each middleware over `gin.New()`, injecting `Authorization`, asserting `c.Writer.Status()` (401/403/200) and that claims/API key are readable via the new accessors.

- [ ] **Step 2: Verify fail.**

- [ ] **Step 3: Implement**:
  ```go
  func RequireAuth(secret []byte) gin.HandlerFunc
  func RequirePermission(secret []byte, action string) gin.HandlerFunc
  func InferenceAuth(secret []byte, svc *Service) gin.HandlerFunc
  func ClaimsFromContext(c *gin.Context) (*Claims, bool)
  func APIKeyFromContext(c *gin.Context) (*controlplane.APIKey, bool)
  func TenantIDFromContext(c *gin.Context) (string, bool)
  ```
  Use `c.Set("claims", claims)` / `c.Get`; `c.AbortWithStatusJSON` for errors preserving the exact error JSON.

- [ ] **Step 4: Pass + vet.**

- [ ] **Step 5: Commit** `refactor(auth): Gin middleware + context accessors`.

---

### Task 3: Inference `Handler` → Gin

**Files:** `go-server/internal/api/handler.go`, `respond.go`.

- [ ] **Step 1: Write failing tests** — adapt `handler_test.go`; add `TestChatCompletionsStreamSSE` over `gin.New()` with a fake loop: assert `Content-Type: text/event-stream`, `data: [DONE]`, and that a `metricWriter` flusher is used.

- [ ] **Step 2: Verify fail.**

- [ ] **Step 3: Implement**:
  - `RegisterRoutes(e *gin.Engine)`: `POST /v1/chat/completions` with `auth.InferenceAuth`; `GET /health`; session routes with `auth.RequireAuth`; `GET /` `/chat` `/keys` → `c.File(filepath.Join(h.uiDir, file))`; `/concepts`.
  - `handleOpenAIChatCompletions(c *gin.Context)`: `c.ShouldBindJSON`, validation, `auth.TenantIDFromContext(c)`.
  - Non-stream: `c.JSON(status, resp)`.
  - Stream: `NewSSEWriter(c.Writer)` (unchanged) + `sse.flusher.Flush()`; on `RouteLabelSetter` use `c.Writer.(observability.RouteLabelSetter)`.
  - Session/UI handlers → `*gin.Context`.
  - `respond.go`: `writeJSON(c, status, v)`, `writeAPIError(c, status, code, msg)`, `writeOpenAIError(c, status, typ, msg)` → `c.JSON` (drop `w http.ResponseWriter`).

- [ ] **Step 4: Pass + vet.**

- [ ] **Step 5: Commit** `refactor(api): inference + session + UI handlers to Gin`.

---

### Task 4: Control plane `Handler` → Gin

**Files:** `go-server/internal/api/controlplane.go`, `controlplane_test.go`, `session_test.go`, `usage_test.go`.

- [ ] **Step 1: Write failing tests** (adapt existing E2E to `gin.New()`; route tests keep asserting status codes + JSON bodies).
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement `RegisterRoutes(e *gin.Engine)`** with per-route `auth.RequirePermission` groups; handlers to `*gin.Context`; path params via `c.Param("id")` instead of `strings.TrimPrefix` where the route is `/api/v1/models/:id` etc. (keep the current prefix-style routes if that preserves behavior; prefer explicit params where the two routes `/models/` GET vs POST differ by method).
- [ ] **Step 4: Pass + vet.**
- [ ] **Step 5: Commit** `refactor(api): control-plane handlers to Gin`.

---

### Task 5: Composition root wiring

**Files:** `go-server/internal/app/app.go`, `registry.go`.

- [ ] **Step 1:** Build `*gin.Engine` in `Run`; force-resolve handlers and call `RegisterRoutes(engine)`; `engine` replaces `mux`.
- [ ] **Step 2:** Keep `http.Server{Handler: engine}` + `server.Close()` graceful shutdown (Gin engine is an `http.Handler`).
- [ ] **Step 3:** `go build ./...`, `go test ./...`.
- [ ] **Step 4:** Boot smoke + **SSE smoke** (login → `curl -N` streaming) to prove `Flush` survives.
- [ ] **Step 5: Commit** `refactor(app): serve via Gin engine`.

---

### Task 6: Documentation

**Files:** `CLAUDE.md`, `docs/TRACKING.md`, `docs/ARCHITECTURE.md`, spec gap note.

- [ ] Mark Phase 3 ✅, update the startup diagram (`gin.Engine`), the tech table (Gin v1.11), and the HTTP layer section (§2.1).

---

## Self-Review Checkpoints

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` clean.
- [ ] Every route + method + status code identical to the `net/http` version (contract tests pass unmodified in intent).
- [ ] SSE streams token-by-token through Gin (`Flush` works); reasoning + tool-call + `[DONE]` frames unchanged.
- [ ] Global middleware order CORS → trace → logging → metrics preserved.
- [ ] `/metrics` still scrapes (Prometheus text format).
- [ ] No `net/http.ServeMux` remains in production code; `net/http` only for `http.Server`, `http.Handler` wrapping, and `http.ServeFile` equivalent.
- [ ] Rate limit, audit, usage metering paths untouched.
