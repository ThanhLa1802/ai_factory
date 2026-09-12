# Phase 2: Data Layer (GORM + gormigrate + repositories) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the raw `pgx` + `goose` data layer with **GORM v1.31.0** + **gormigrate v2.1.5** behind **repository interfaces**, without changing any HTTP contract or exported service method. Every existing integration test must keep passing, and the app must boot against the existing (already goose-migrated) Postgres.

**Architecture:** Phase 2 of the design `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` (§4.1, §6.2, §8). New `internal/infrastructure/database` owns the GORM connection + migration runner; `internal/migrations` holds Go migrations converted 1:1 from the 6 goose SQL files (table/column names **unchanged**). Repositories are defined as interfaces and implemented with GORM inside the existing `controlplane`/`session` packages; the physical `services/*` split is deferred to **Phase 4**. Domain/JSON structs stay as-is, so `internal/api` is untouched.

**Tech Stack:** Go 1.25.7, `gorm.io/gorm` v1.31.0, `gorm.io/driver/postgres`, `github.com/go-gormigrate/gormigrate/v2` v2.1.5.

## Decisions (locked before Phase 2)

- **D-P2-1 — Test DB:** live Postgres via `AI_FACTORY_DATABASE_URL`; tests `t.Skip` when unset (matches every existing integration test). No sqlite/testcontainers.
- **D-P2-2 — Package layout:** models + repositories live in the current packages (`internal/controlplane`, `internal/session`) this phase; Phase 4 relocates them into `services/{iam,serving,usage,inference}`. Only infrastructure moves now.
- **D-P2-3 — Keep the `Service` API frozen.** All 32 exported `*controlplane.Service` methods keep their exact signatures; `runtime.DeploymentStore` and `api` interfaces keep compiling.
- **D-P2-4 — Domain vs model separation.** GORM entities (`*Row` types) are distinct from the domain/JSON structs; repositories map between them. `User` (joins `tenant_memberships`) requires a custom row.
- **D-P2-5 — `serializer:json`** for JSONB columns (`metadata_json`, `command`, `environment`, `config_schema`, `spec_json`, `tool_calls`) instead of `datatypes.JSON`.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- **No HTTP/behaviour change.** `go test ./...` green; boot + `/health` + one chat turn still work.
- **Migration IDs** mirror the goose numeric versions: `0001_init`, `0002_deployment_workload_ref`, `0003_idempotency_keys`, `0004_chat_history`, `0006_usage_events`, `0007_session_title` (there is no 0005).
- **goose adoption:** if `goose_db_version` exists and the gormigrate table is empty, mark the matching migrations as already applied before running `Up`. Fresh DB runs all `Up`. Never `Down` on a live/adopted DB.
- Table/column names must stay byte-identical to the goose schema (no `AutoMigrate`).
- Timestamps UTC; UUID primary keys generated in Go (`uuid.NewString()`), not DB defaults — same as today.
- Security: never log secrets/API keys.
- TDD: failing test → implement → pass → `go vet ./...` → commit.

## File Structure

| File | Responsibility |
|---|---|
| `go-server/internal/infrastructure/database/database.go` | `DB` wrapper around `*gorm.DB`; `Open(dsn)`, `Gorm()`, `Close()` |
| `go-server/internal/infrastructure/database/migrate.go` | gormigrate runner + goose-adoption bootstrap |
| `go-server/internal/infrastructure/database/migrate_test.go` | idempotent migrate + version→ID mapping |
| `go-server/internal/migrations/migrations.go` | `All() []*gormigrate.Migration` |
| `go-server/internal/migrations/0001_init.go` … `0007_session_title.go` | 6 Go migrations (DDL copied verbatim) |
| `go-server/internal/controlplane/models.go` | GORM row types + `TableName()` + mappers |
| `go-server/internal/controlplane/repositories.go` | repository interfaces + `Repositories` bundle |
| `go-server/internal/controlplane/repository_iam.go` | tenants, users, api keys |
| `go-server/internal/controlplane/repository_serving.go` | models/versions, templates/versions, deployments/revisions/endpoints |
| `go-server/internal/controlplane/repository_usage.go` | quotas, usage |
| `go-server/internal/controlplane/repository_idempotency.go` | idempotency keys |
| `go-server/internal/controlplane/service.go` | `Service` now holds the `Repositories` bundle |
| `go-server/internal/session/store_gorm.go` | GORM-backed `Store` (replaces `PGStore`) |
| `go-server/internal/session/models.go` | `sessionRow`, `messageRow` |
| `go-server/internal/app/registry.go` | wire `database` + repositories + services; drop `internal/db` |
| `go-server/internal/db/` | **deleted** (pgx + goose) at the end of the phase |
| `go-server/go.mod` | add gorm, postgres driver, gormigrate |

---

### Task 1: GORM connection + gormigrate migrations

**Files:**
- Create: `go-server/internal/infrastructure/database/database.go`
- Create: `go-server/internal/infrastructure/database/migrate.go`
- Create: `go-server/internal/infrastructure/database/migrate_test.go`
- Create: `go-server/internal/migrations/migrations.go` + 6 migration files
- Modify: `go-server/go.mod`

**Interfaces:**
- Produces: `database.Open(dsn string) (*DB, error)`, `(*DB).Gorm() *gorm.DB`, `(*DB).Close() error`, `database.Migrate(db *gorm.DB) error`, `migrations.All() []*gormigrate.Migration`.

- [ ] **Step 1: Add dependencies**
  ```bash
  cd go-server
  go get gorm.io/gorm@v1.31.0
  go get gorm.io/driver/postgres@latest
  go get github.com/go-gormigrate/gormigrate/v2@v2.1.5
  ```

- [ ] **Step 2: Write the failing tests** — `migrate_test.go`:
  - `TestGooseVersionToID` (pure): `1→"0001_init"`, `4→"0004_chat_history"`, `7→"0007_session_title"`, unknown `5→""`.
  - `TestMigrateIdempotent` (integration, skip w/o DSN): `Open` → `Migrate` twice → both nil; then query `information_schema.tables` for `tenants`,`messages`,`usage_events`.

- [ ] **Step 3: Run to verify fail** (`go test ./internal/infrastructure/database/` → undefined).

- [ ] **Step 4: Implement `database.go`** — `gorm.Open(postgres.Open(dsn), &gorm.Config{...})`, ping, wrap `*gorm.DB`; `Close` closes the underlying `*sql.DB`.

- [ ] **Step 5: Implement the 6 migrations.** Copy each goose `Up` DDL verbatim into a `gormigrate.Migration{ID, Migrate: func(tx *gorm.DB) error {...}}`. `Down` is included but never called on live DBs. `0001_init` also `CREATE EXTENSION IF NOT EXISTS pgcrypto`. Use `tx.Exec(...)` with the raw SQL string. `migrations.All()` returns them in order.

- [ ] **Step 6: Implement `Migrate`** with:
  ```go
  m := gormigrate.New(db, gormigrate.DefaultOptions, migrations.All())
  m.SetTableName("schema_migrations") // override before Run
  err := adoptGoose(db, m)            // no-op unless goose_db_version exists
  return m.Migrate()
  ```
  `adoptGoose`: if `to_regclass('goose_db_version')` is not null and `schema_migrations` empty, `SELECT version_id FROM goose_db_version WHERE is_applied`; map via `gooseVersionToID`; insert IDs into `schema_migrations` (`ON CONFLICT DO NOTHING`). Abstract the map as `gooseVersionToID(int64) string`.

- [ ] **Step 7: Run to verify pass** (integration runs against compose Postgres with `AI_FACTORY_DATABASE_URL` set; otherwise skipped).

- [ ] **Step 8: Vet + commit** `feat(database): GORM connection + gormigrate migrations (goose adoption)`.

---

### Task 2: GORM models + mappers

**Files:**
- Create: `go-server/internal/controlplane/models.go`
- Create: `go-server/internal/controlplane/models_test.go`

**Interfaces:** row types (`tenantRow`, `userRow`, `membershipRow`, `apiKeyRow`, `quotaRow`, `modelRow`, `modelVersionRow`, `templateRow`, `templateVersionRow`, `deploymentRow`, `revisionRow`, `endpointRow`, `usageRow`, `idempotencyRow`) each with `TableName()`; `serializer:json` tags on JSONB columns; `nullableUUID` reused for nullable FKs.

- [ ] **Step 1: Write a schema-parity test** (integration): after `Migrate`, load `information_schema.columns` for `deployments` and assert the `deploymentRow` GORM-resolved column set (via `db.Migrator().ColumnTypes`) equals the live table's columns. This catches tag/column drift without `AutoMigrate`.
- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement rows + `TableName()`** mirroring `0001`–`0007` exactly (`workload_ref` present; sessions has `title`; messages JSONB `tool_calls`).
- [ ] **Step 4: Implement mappers** `toTenant(row) Tenant`, `toDeployment(row) Deployment`, etc. (and `toUser` sets `Role`/`TenantID` from the membership row).
- [ ] **Step 5: Pass + vet.**
- [ ] **Step 6: Commit** `feat(controlplane): GORM models + domain mappers`.

---

### Task 3: IAM repositories + rewrite Service users/tenants/api-keys

**Files:**
- Create: `go-server/internal/controlplane/repositories.go` (interfaces + `Repositories` bundle; starts with IAM)
- Create: `go-server/internal/controlplane/repository_iam.go`
- Modify: `go-server/internal/controlplane/users.go` → move `Service` to `service.go`, hold `Repositories`
- Modify: `go-server/internal/controlplane/users_test.go` (construct repos over test DB)

**Interfaces:**
```go
type TenantRepository interface {
    Create(ctx context.Context, t *Tenant) error
    List(ctx context.Context) ([]Tenant, error)
}
type UserRepository interface {
    Create(ctx context.Context, u *User, passwordHash string) error   // users + membership tx
    GetByUsername(ctx context.Context, username string) (*User, string, error)
}
type APIKeyRepository interface {
    Create(ctx context.Context, k *APIKey, keyHash string) error
    GetByHash(ctx context.Context, keyHash string) (*APIKey, error)
    ListByTenant(ctx context.Context, tenantID string) ([]APIKey, error)
    Delete(ctx context.Context, id, tenantID string) error            // ErrNotFound when 0 rows
}
```
- [ ] **Step 1: Failing tests** — a `repos_test.go` (integration) that runs the same assertions as `TestTenantUserAPIKeyIntegration` but through `NewService(Repositories{...})`.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement GORM repos** (`CreateUser` uses `db.Transaction` for users + membership; `GetByUsername` uses `Joins` + `Select`).
- [ ] **Step 4: Rewrite `Service`** so the 8 IAM methods delegate to the repos; keep signatures. `NewService(Repositories) *Service`.
- [ ] **Step 5: Existing `users_test.go` integration passes unchanged in behaviour** (update construction only).
- [ ] **Step 6: Vet + commit** `refactor(controlplane): IAM repositories (GORM) behind interfaces`.

---

### Task 4: Serving repositories + rewrite catalog/deployments

**Files:** `repository_serving.go`; modify `catalog.go`, `deployment.go`, `catalog_test.go`, `deployment_test.go`.

**Interfaces:** `ModelRepository`, `TemplateRepository`, `DeploymentRepository` covering all 17 serving methods (Create/List/Get model+version, template+version, deployment list/get/resolve, transition, revisions, workload ref, endpoint).

- [ ] **Step 1: Failing tests** replicating `deployment_test.go` + `catalog_test.go` through `NewService`.
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement repos.** `ResolveDeployment` uses `Joins("JOIN model_versions ... JOIN models ...").Where("models.name = ? AND deployments.tenant_id = ? AND deployments.status = ?").Order("deployments.created_at DESC").First(...)`; map `gorm.ErrRecordNotFound → ErrNotFound`. `CreateRevision` computes `MAX(revision)+1` inside a transaction.
- [ ] **Step 4: Delegate Service methods; keep `runtime.DeploymentStore` satisfied.**
- [ ] **Step 5: `go test ./internal/runtime/ ./internal/controlplane/` green.**
- [ ] **Step 6: Commit** `refactor(controlplane): serving repositories (GORM)`.

---

### Task 5: Usage / quota / idempotency repositories

**Files:** `repository_usage.go`, `repository_idempotency.go`; modify `usage.go`, `quota.go`, `idempotency.go` + their tests.

**Interfaces:** `QuotaRepository` (Upsert, List), `UsageRepository` (Record, Summary, Daily, ByModel), `IdempotencyRepository` (Save, Resolve).

- [ ] **Step 1: Failing tests** (`usage_test.go`, `quota_test.go`, `idempotency_test.go` through Service).
- [ ] **Step 2..4:** Implement. Aggregations use `Select("COALESCE(SUM(...),0)")` + `Scan`; `UsageDaily` groups by `(created_at AT TIME ZONE 'UTC')::date`. Upsert uses `clause.OnConflict{Columns: ..., DoUpdates: ...}`. Idempotency `Save` uses `clause.OnConflict{DoNothing: true}`.
- [ ] **Step 5: Pass + vet.**
- [ ] **Step 6: Commit** `refactor(controlplane): usage/quota/idempotency repositories (GORM)`.

---

### Task 6: Session GORM store

**Files:** `go-server/internal/session/store_gorm.go`, `session/models.go`; delete `store.go`'s pgx impl (keep the `Store` interface + `SessionSummary`); modify `store_test.go`.

**Interfaces:** `NewGormStore(db *gorm.DB) *GormStore` implementing `session.Store`.

- [ ] **Step 1: Failing tests** — `store_test.go` runs against `NewGormStore` (same assertions: upsert/forbidden, load+order, append, list with title fallback, rename, delete).
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement.** `UpsertSession` uses `clause.OnConflict{Columns: id, DoUpdates: assignment.Columns(...), Where: "sessions.tenant_id = excluded.tenant_id"}` then checks `RowsAffected == 0 → ErrSessionForbidden`. `LoadSession` uses `user_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid` (raw `Where`). `AppendMessage` inserts a `messageRow`.
- [ ] **Step 4: Pass + vet.**
- [ ] **Step 5: Commit** `refactor(session): GORM-backed Store (`NewGormStore`)`.

---

### Task 7: Wire composition root + delete pgx `internal/db`

**Files:** modify `internal/app/registry.go`, `internal/app/app.go`; delete `internal/db/` (`db.go`, `db_test.go`, `migrations/`); update any test imports.

- [ ] **Step 1: Update registry** — `database.Open` from `cfg.DatabaseURL`; `database.Migrate`; build `controlplane.Repositories{...}`; `controlplane.NewService(repos)`; `session.NewGormStore(db.Gorm())`. Register `database` as a DI singleton implementing `Closer`.
- [ ] **Step 2: Force-resolve list** unchanged (still resolves `db` name → now the GORM DB).
- [ ] **Step 3: `go build ./...`** must succeed with no `internal/db` references.
- [ ] **Step 4: `go test ./...`** green (integration tests skip if DSN unset).
- [ ] **Step 5: Boot smoke test** against compose Postgres (existing goose DB → adoption path): `/health` 200, seed runs, `/metrics` 200.
- [ ] **Step 6: Commit** `refactor(app): wire GORM data layer; remove pgx/goose db package`.

---

### Task 8: Documentation

**Files:** `CLAUDE.md`, `docs/TRACKING.md`, `docs/ARCHITECTURE.md`, spec gap note.

- [ ] Update structure/config sections (GORM, gormigrate, `infrastructure/database`, `migrations/`), mark Phase 2 ✅ in TRACKING + update log, and note the deferred `services/*` split (Phase 4).

---

## Self-Review Checkpoints

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` clean.
- [ ] `internal/db` deleted; no `pgxpool`/`goose` imports remain in non-test production code (`session`, `controlplane`, `app`, `infrastructure`).
- [ ] All 32 `*controlplane.Service` methods and the `runtime.DeploymentStore` interface compile unchanged.
- [ ] Booting against the already-goose-migrated dev DB does **not** re-run DDL (adoption path verified by smoke test).
- [ ] No `AutoMigrate` anywhere.
- [ ] JSON column round-trips (tool_calls, spec, metadata) preserved.
- [ ] JWT/agentic/SSE paths untouched — Phase 2 is data-layer only.
