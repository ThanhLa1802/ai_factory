# Phase 5: Multi-binary (`cmd/{server,worker,migrate,seed}`) — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the single `cmd/server` binary into four role-specific binaries over the same codebase, with `services.api` / `services.worker` config flags choosing which roles a process runs — so the deployment worker can run **independently** of the API node.

**Architecture:** Phase 5 of `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` (§5, §8). The composition root `internal/app` stays the only place that knows every component; binaries are thin entry points that load config, set their role, and call `app`. `cmd/migrate`/`cmd/seed` reuse the same config + database providers. Outbox/cache-aside stays **Phase 6**.

**Tech Stack:** Go 1.25.7. No new third-party dependency — pure composition/binary phase.

---

## Decisions (locked before Phase 5)

- **D-P5-1 — Two role flags, not one per module.** `services.api` (HTTP server + inference + control plane) and `services.worker` (deployment worker consumer). Both default `true`, so `cmd/server` keeps today's behaviour (API + in-process worker). Env override `AI_FACTORY_SERVICES_API` / `AI_FACTORY_SERVICES_WORKER`. Per-service flags beyond these are **deferred** — two roles are what the goal ("worker chạy độc lập") needs.
- **D-P5-2 — `cmd/worker` is worker-only and headless.** It forces `Services.API=false, Services.Worker=true`, does **not** seed, and opens **no HTTP server** (spec §12.4, user decision 2026-09-12: log + graceful shutdown only). It blocks on SIGINT/SIGTERM then shuts the container down.
- **D-P5-3 — `cmd/server` honours both flags.** If an operator sets `services.worker=false`, the server runs API-only and an external worker is required. Default remains both-on (backward compatible).
- **D-P5-4 — Lazy DI does the heavy lifting.** `RegisterAll` gates the API-only providers (inference client/scheduler/loop, inference manager, all `http.*` handlers) behind `Services.API`, and the worker provider behind `Services.Worker`. Cheap control-plane services (iam/serving/usage) are always registered but never constructed unless resolved (DI is lazy). No provider is built for a disabled role.
- **D-P5-5 — `cmd/migrate` and `cmd/seed` are separate runners, not roles.** `app.RunMigrate(cfg)` opens the DB and runs gormigrate; `app.RunSeed(ctx, cfg)` opens a minimal container and runs `seedAdmin` + `seedDemo`. Neither starts HTTP or the worker. Seeding stays in `app` (D-P4-8).
- **D-P5-6 — Contract freeze.** `cmd/server` stays byte-identical on the wire: same flags, same routes, same seeding, same in-process worker when Kafka is reachable.
- **D-P5-7 — No DB/migration change.** `cmd/migrate` calls the existing `database.Migrate` (gormigrate + goose adoption); no migration edits.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- No HTTP/behaviour change for `cmd/server`; no route/JSON/SSE change.
- No DB change; migrations untouched.
- Security: never log tokens/prompts.
- TDD: failing test → implement → pass → `go vet ./...` → `go test ./...`.
- Commit style: `refactor(cmd): ...` / `feat(config): ...`.

## Role matrix

| Binary | `services.api` | `services.worker` | Seeds | HTTP | Worker |
|---|---|---|---|---|---|
| `cmd/server` (default) | true | true | yes | yes | yes (if Kafka up) |
| `cmd/server --` + `services.worker=false` | true | false | yes | yes | no |
| `cmd/worker` | false | true | no | no | yes (if Kafka up) |
| `cmd/migrate` | — | — | no | no | no |
| `cmd/seed` | false | false | yes | no | no |

---

### Task 1: Config — `services.api` / `services.worker`

**Files:** `internal/config/config.go`, `internal/config/config_test.go`, `configs/config.yaml`.

- [x] **Step 1: Write failing test** — defaults `Services.API == true && Services.Worker == true`; `AI_FACTORY_SERVICES_API=false` override yields `false`; YAML `services: {worker: false}` respected.
- [x] **Step 2: Verify fail.**
- [x] **Step 3: Implement** `type ServicesConfig struct { API, Worker bool }`; add `Services ServicesConfig` to `Config`; `v.SetDefault("services.api", true)`, `v.SetDefault("services.worker", true)`; populate from `v.GetBool(...)`. Add the block to `configs/config.yaml`.
- [x] **Step 4:** `go test ./internal/config/`.
- [x] **Step 5: Commit** `feat(config): add services.api/services.worker role flags`.

---

### Task 2: Composition root — role gating + runners

**Files:** `internal/app/registry.go`, `internal/app/app.go`, `internal/app/runner.go` (new).

**Interfaces produced:**
```go
// app.go
func (a *App) Run() error          // seeds only if API; starts worker if enabled; serves HTTP only if API; else waits for signal
func RunMigrate(cfg *config.Config) error
func RunSeed(ctx context.Context, cfg *config.Config) error
```
- [x] **Step 1: Write failing test** (`internal/app/role_test.go`, no DB): with `Services{API:false,Worker:false}`, `RegisterAll` succeeds and `Resolve("http.handler")` / `Resolve("deployment.worker")` return *not-registered*; with `Services{API:true,Worker:false}` those same resolves are *registered* (error is not "not registered").
- [x] **Step 2: Verify fail.**
- [x] **Step 3: Implement gating** in `RegisterAll`: move `inference.client`, `batch.scheduler`, `tool.executor`, `inference.loop`, `inference.manager` and the four `http.*` providers under `if cfg.Services.API`; wrap `deployment.worker` in `if cfg.Services.Worker`. Keep logger/db/redis/limiter/bus/iam/serving/usage **always** registered.
- [x] **Step 4: Implement lifecycle:** `NewAppFromContainer` force-resolves role-dependent names; `Run` seeds only when API, starts worker when enabled, serves HTTP only when API, otherwise logs + waits for signal.
- [x] **Step 5: Implement runners:** `RunMigrate` (config → `database.Open` → `database.Migrate`) and `RunSeed` (minimal `RegisterAll` with both roles off → resolve `iam`/`serving` → `seedAdmin`+`seedDemo`).
- [x] **Step 6:** `go build ./...`, `go vet ./...`, `go test ./...`.
- [x] **Step 7: Commit** `refactor(app): role-gated wiring + migrate/seed runners`.

---

### Task 3: Binaries

**Files:** `cmd/worker/main.go`, `cmd/migrate/main.go`, `cmd/seed/main.go`; touch `cmd/server/main.go` only if needed (it is not).

- [x] **Step 1: `cmd/worker`** — flags `--config`, `--inference-addr`; `config.Load` → force `API=false, Worker=true` → `SetupLogger` → `RegisterAll` → `NewAppFromContainer(cfg, 0)` → `Run()`.
- [x] **Step 2: `cmd/migrate`** — flags `--config`; `config.Load` → `SetupLogger` → `app.RunMigrate(cfg)`; log + exit non-zero on error.
- [x] **Step 3: `cmd/seed`** — flags `--config`; `config.Load` → `SetupLogger` → `app.RunSeed(ctx, cfg)`; log + exit.
- [x] **Step 4:** `go build ./...`.
- [x] **Step 5: Commit** `feat(cmd): add worker/migrate/seed binaries`.

---

### Task 4: Container build

**Files:** `go-server/Dockerfile`, `deployments/docker-compose.yml`.

- [x] **Step 1:** Build all four binaries in the builder stage (`/out/{server,worker,migrate,seed}`) and copy them into the runtime image.
- [x] **Step 2:** Add a `worker` compose service (same image, `entrypoint: /app/worker`, `services.worker=true`, `services.api=false`, depends on postgres + kafka) — commented note that Kafka's advertised listener is host-only.
- [x] **Step 3: Commit** `build(docker): ship multi-binary image + worker service`.

---

### Task 5: Documentation + verification

**Files:** `CLAUDE.md`, `docs/TRACKING.md`, spec status.

- [x] Mark Phase 5 ✅; update the project-structure tree (`cmd/{server,worker,migrate,seed}`), the "Running" section (worker/migrate/seed commands), and the roadmap line.
- [x] Update the TRACKING update-log + phase status; note the only remaining phase is Phase 6 (outbox/cache-aside).
- [x] Record spec §12.4 decision (worker headless).

## Self-Review Checkpoints

- [x] `go build ./...`, `go vet ./...`, `go test ./...` clean.
- [x] `cmd/server` defaults unchanged (API + worker + seed).
- [x] `cmd/worker` with `services.api=false` never resolves an `http.*` provider.
- [x] `cmd/migrate` applies migrations without starting any server.
- [x] `cmd/seed` seeds admin + demo without starting a server.
- [x] No DB migration change; integration tests still skip without `AI_FACTORY_DATABASE_URL`.
- [x] Role flags overridable by env (`AI_FACTORY_SERVICES_API`, `AI_FACTORY_SERVICES_WORKER`).
