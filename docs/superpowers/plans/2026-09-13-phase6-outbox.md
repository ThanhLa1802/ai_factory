# Phase 6a: Transactional Outbox — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop losing deployment events when the process dies between the DB commit and the Kafka publish. Write the event into an `outbox` table **in the same transaction** as the deployment row; a background publisher drains the outbox to the event bus and marks rows sent.

**Scope (this pass):** transactional outbox only (tracer bullet). Cache-aside, distributed lock, and usage aggregate stay for a later Phase 6 pass (spec `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` §7).

**Architecture:** new `internal/infrastructure/outbox` (row + store + publisher); the serving deployment repository gains a `CreateWithEvent` method that inserts the deployment and the outbox row in one `db.Transaction`; the serving handler no longer publishes directly. Publisher runs on the API node and is stopped by the DI lifecycle.

---

## Decisions (locked before this pass)

- **D-P6-1 — Outbox only, tracer bullet.** Only the user-triggered deployment events (`deployment_created`, `deployment_stop_requested`) move to the outbox. The worker's internal state events (`deployment_ready`/`failed`/`stopped`) keep publishing directly — they are emitted by the consumer process, not by a DB write.
- **D-P6-2 — Transaction boundary at the repository.** A new `DeploymentRepository.CreateWithEvent(ctx, d, topic, ev)` runs `db.Transaction` and inserts the deployment + outbox row. No `UnitOfWork`/`TxManager` abstraction is introduced; the service still never sees `*gorm.DB`.
- **D-P6-3 — Full envelope stored as JSONB.** The outbox row stores the serialized `message.Event`, so the publisher re-emits it byte-for-byte (same `event_id`/`trace_id`).
- **D-P6-4 — At-least-once publisher.** Poll unpublished rows ordered by `created_at`, publish, then set `published_at`. On failure increment `attempts` + `last_error` and leave the row for the next poll. Consumer-side idempotency (worker state-machine guard) already makes replays safe.
- **D-P6-5 — No config change.** Fixed poll interval (500 ms) + batch size (100); no new config key.
- **D-P6-6 — Create now fails atomically.** If the outbox insert fails, the deployment insert rolls back and the handler returns 500 (previously: deployment persisted + marked FAILED, 503).
- **D-P6-7 — Worker nodes do not run the publisher.** The outbox is written on API nodes only; register `outbox.store`/`outbox.publisher` behind `services.api`.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- No new third-party dependency.
- No HTTP route/JSON/SSE shape change for existing responses.
- Security: never log tokens/prompts; outbox stores the event envelope only.
- TDD: failing test → implement → pass → `go vet ./...` → `go test ./...`.
- Commit style: `feat(outbox): ...` / `refactor(serving): ...`.

## Role matrix

| Binary | `outbox.store` | `outbox.publisher` |
|---|---|---|
| `cmd/server` | yes | yes (drains in background) |
| `cmd/worker` | no | no |
| `cmd/migrate` | n/a (migration only) | no |
| `cmd/seed` | no | no |

---

### Task 1: Migration — `outbox`

**Files:** `internal/migrations/0008_outbox.go` (new), `internal/migrations/migrations.go`.

- [x] **Step 1:** Add `0008_outbox` creating `outbox(id uuid pk, topic text, event_id text, event_type text, tenant_id text, resource_id text, event jsonb, created_at timestamptz, published_at timestamptz null, attempts int default 0, last_error text default '')` + partial index on unpublished `created_at`.
- [x] **Step 2:** Append `M0008Outbox()` to `migrations.All()`.
- [x] **Step 3:** `go test ./internal/infrastructure/database/` (idempotent migrate) with `AI_FACTORY_DATABASE_URL`.

---

### Task 2: `internal/infrastructure/outbox` — record + store

**Files:** `internal/infrastructure/outbox/outbox.go` (new).

**Interfaces produced:**
```go
type Store struct{ /* db */ }
func NewStore(db *gorm.DB) *Store
func (s *Store) Enqueue(ctx context.Context, topic string, ev message.Event) error
func EnqueueTx(tx *gorm.DB, topic string, ev message.Event) error // used inside a repo tx
func (s *Store) FetchBatch(ctx context.Context, limit int) ([]Record, error)
func (s *Store) MarkPublished(ctx context.Context, id string) error
func (s *Store) MarkFailed(ctx context.Context, id string, cause error) error
```
- [x] **Step 1:** Define `Record` (`TableName() == "outbox"`) mapping the migration columns.
- [x] **Step 2:** Implement `EnqueueTx` (marshal envelope → insert) and `Enqueue` (own connection), `FetchBatch` (`published_at IS NULL ORDER BY created_at LIMIT n`), `MarkPublished`, `MarkFailed` (`attempts = attempts + 1`).
- [x] **Step 3:** Integration test (`outbox_test.go`, Postgres-gated): enqueue two events in a tx, fetch batch sees them in order, mark published removes from batch, mark failed bumps attempts.

---

### Task 3: Outbox publisher

**Files:** `internal/infrastructure/outbox/publisher.go` (new).

**Interfaces produced:**
```go
func NewPublisher(store *Store, producer message.Producer, log *slog.Logger) *Publisher
func (p *Publisher) Start(ctx context.Context)      // background poll loop
func (p *Publisher) DrainOnce(ctx context.Context) (int, error) // deterministic flush (tests)
func (p *Publisher) Close() error                  // stops the loop (DI Closer)
```
- [x] **Step 1:** `DrainOnce` fetches a batch, unmarshals each envelope, publishes, marks published (or failed), returns count.
- [x] **Step 2:** `Start` runs `DrainOnce` on a ticker until the context is cancelled/`Close`.
- [x] **Step 3:** Test with a fake `message.Producer`: publish → row marked published; failing producer → row stays unpublished with `attempts` incremented.

---

### Task 4: Serving — transactional write

**Files:** `internal/services/serving/repositories.go`, `repository_serving.go`, `deployment.go`.

- [x] **Step 1:** Add `EventSink` interface + `CreateWithEvent` to `DeploymentRepository`.
- [x] **Step 2:** Implement `deploymentRepo.CreateWithEvent` with `db.Transaction` inserting the deployment then `outbox.EnqueueTx(tx, topic, ev)`.
- [x] **Step 3:** Add `Service.CreateDeploymentWithEvent(ctx, d, createdBy)` that assigns ID/status, builds the `deployment_created` envelope, and calls the repo.

---

### Task 5: Serving handler — publish via outbox

**Files:** `internal/services/serving/handlers.go`.

- [x] **Step 1:** Replace the `message.Producer` dependency with `EventSink` (`Enqueue`).
- [x] **Step 2:** `handleCreateDeployment` calls `CreateDeploymentWithEvent` (no direct publish); 500 on error.
- [x] **Step 3:** `handleDeploymentAction` start/stop enqueue through the sink; 503 on enqueue error.

---

### Task 6: Composition root

**Files:** `internal/app/registry.go`, `internal/app/app.go`.

- [x] **Step 1:** Register `outbox.store` + `outbox.publisher` behind `services.api`; wire `http.serving` with the store as `EventSink`.
- [x] **Step 2:** Force-resolve the outbox providers for API nodes; start the publisher in `App.Run` after seeding.
- [x] **Step 3:** `go build ./...`, `go vet ./...`.

---

### Task 7: Tests + docs

- [x] Adapt `internal/app/e2e_test.go` (`mountServing` takes an `EventSink`; drain the publisher after POST before asserting).
- [x] `go test ./...` clean (integration tests skip without `AI_FACTORY_DATABASE_URL`).
- [x] Update `docs/TRACKING.md` + `CLAUDE.md`; record the deferred Phase 6 patterns.

## Self-Review Checkpoints

- [x] `cmd/server` boots and serves; outbox publisher drains rows.
- [x] `cmd/worker` registers no outbox provider.
- [x] A deployment + its outbox row commit atomically (rollback on failure).
- [x] Event published from outbox carries the original `event_id`.
- [x] No route/JSON/SSE change.
