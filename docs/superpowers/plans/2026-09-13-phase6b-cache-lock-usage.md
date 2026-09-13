# Phase 6b: Cache-aside + distributed lock + usage aggregate — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the reliability patterns (spec §7) left after the outbox (Phase 6a): a Redis distributed lock primitive, cache-aside for API-key authentication (with invalidation), and a usage aggregate that buffers counters in Redis and flushes them into a dedicated `usage_daily` table.

**Architecture:** primitives live in `internal/infrastructure/cache` (lock, generic KV, usage counter). Services keep consumer-defined ports: `iam.ByteCache` (implemented by `cache.KV`), `usage.Locker` (implemented by `cache.Lock`), `usage.Counter` (implemented by `cache.UsageCounter`). Wiring stays in `internal/app`; the usage flush worker is an API-node background loop guarded by the distributed lock.

**Tech stack:** Go 1.25.7, go-redis v9, miniredis for unit tests, Postgres for integration tests. No new production dependency.

---

## Decisions (locked before Phase 6b)

- **D-P6b-1 — Lock is SET NX + owner token.** `Acquire` sets a random token with `SET NX PX ttl`; `Release` deletes only if the token still matches (WATCH compare-and-delete), so a holder never releases another holder's lock.
- **D-P6b-2 — Cache-aside at API-key authentication.** `AuthService.AuthenticateAPIKey` caches the resolved `APIKey` (JSON) under `apikey:<sha256>` for 5 minutes. Inactive/expired keys are never cached. `DeleteAPIKey` returns the deleted hash so the handler invalidates `apikey:<hash>`.
- **D-P6b-3 — Cache is best-effort.** A Redis error on get/set/delete is logged/ignored and falls back to the DB; the cache never fails authentication.
- **D-P6b-4 — Usage write path buffers.** When a counter is configured, `RecordUsage` increments Redis counters keyed by `usage:<tenant>:<model>:<day>` (TTL 48h); the DB `usage_events` row is no longer written on that path. Without a counter (tests, seeder) the legacy direct insert is kept.
- **D-P6b-5 — Separate aggregate table.** Migration `0009_usage_daily` adds `usage_daily (tenant_id, model, day, prompt_tokens, completion_tokens, requests, updated_at)` keyed by `(tenant_id, model, day)`. The flush worker drains Redis buckets and upserts the sums.
- **D-P6b-6 — One flusher per cluster.** The flush loop acquires `usage:flush` (lock) for one cycle; a second replica skips the cycle. Flush interval 30s (fixed).
- **D-P6b-7 — Reads follow the configured mode.** With a counter, `UsageSummary`/`Daily`/`ByModel` read `usage_daily` (flush lag ≤ one interval). Without a counter they keep reading `usage_events` (existing tests/seeder unchanged).

## Global Constraints

- No HTTP route/JSON/SSE shape change.
- Security: never log raw API keys or prompts; cache stores the key hash, not the raw key.
- TDD: failing test → implement → pass → `go vet ./...` → `go test ./...`.
- Commit style: `feat(cache): ...` / `feat(iam): ...` / `feat(usage): ...`.

---

### Task 1: Distributed lock

**Files:** `internal/infrastructure/cache/lock.go`, `lock_test.go`.

- [x] `Lock.Acquire(ctx, key, ttl) (token string, ok bool, err error)` via `SET NX PX` with a random token.
- [x] `Lock.Release(ctx, key, token)` via a Lua compare-and-delete; releasing a lock you don't own is a no-op.
- [x] miniredis tests: mutual exclusion, wrong-token release is a no-op, TTL expiry lets a new holder in.

### Task 2: Cache-aside API keys

**Files:** `internal/infrastructure/cache/kv.go` (+test); `internal/services/iam/{auth,service,users,repositories,repository_iam,handlers}.go`.

- [x] `cache.KV`: `Get`/`Set`/`Delete` over `[]byte` with TTL.
- [x] `iam.ByteCache` port + `NewAuthServiceWithCache`; `AuthenticateAPIKey` checks cache → DB → populate; skips caching inactive/expired.
- [x] `DeleteAPIKey` returns the deleted `key_hash` (`DELETE ... RETURNING`); handler calls `authSvc.InvalidateAPIKey(ctx, hash)`.
- [x] Tests: cache hit avoids the store; invalidation forces a DB read; Redis error falls back to the store.

### Task 3: Usage aggregate

**Files:** `internal/migrations/0009_usage_daily.go` (+`migrations.go`); `internal/infrastructure/cache/usage_counter.go` (+test); `internal/services/usage/{models,repositories,repository_usage,service,usage,flusher}.go` (+tests).

- [x] Migration `usage_daily` + unique `(tenant_id, model, day)`.
- [x] `cache.UsageCounter`: `Incr` (HINCRBY + EXPIRE) and `Drain` (HGETALL + DEL, per key) returning buckets; `Snapshot` not required.
- [x] `usage.Counter`/`usage.Locker` ports; `AggregateRepository` (`Upsert`, `Summary`, `Daily`, `ByModel` over `usage_daily`).
- [x] `Service`: optional counter switches the write path (buffered) and read path (`usage_daily`).
- [x] `usage.Flusher`: `Start`/`FlushOnce`/`Close`; `FlushOnce` takes `usage:flush`, drains, upserts; no-op on lock miss.
- [x] Tests: counter increments/drains; flusher upserts and is idempotent under a held lock; service buffer→flush→aggregate read end to end.

### Task 4: Wiring + docs

**Files:** `internal/app/registry.go`, `app.go`; `docs/TRACKING.md`, `CLAUDE.md`.

- [x] Register `cache.kv`, `cache.lock`, `usage.counter`; build `iam.auth` with the KV cache; build `usage` with the counter; register + start `usage.flusher` for API nodes only.
- [x] `go build ./...`, `go vet ./...`, `go test ./...` clean.
- [x] Record Phase 6b; note that Phase 6 is now complete.

## Self-Review Checkpoints

- [x] Lock: two holders cannot hold the same key; wrong-token release does not free it.
- [x] API-key cache hit avoids Postgres; delete invalidates immediately.
- [x] Usage counters flush into `usage_daily`; a second flusher skips while the lock is held.
- [x] `cmd/worker` registers no flusher/HTTP; no route change.
