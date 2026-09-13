# CLAUDE.md — AI Factory

A learning project simulating a Claude Code / ChatGPT server, comprising an inference engine, an agentic loop, and an API server.

> **Further reading:** [`README.md`](README.md) (public overview), [`docs/TRACKING.md`](docs/TRACKING.md) (progress tracker — where the project currently is), [`docs/LEARNING_ROADMAP.md`](docs/LEARNING_ROADMAP.md) (project-specific learning roadmap — 2 tracks: backend/platform + self-written inference), `docs/ARCHITECTURE.md` (detailed architecture, deep-dive into each component + integration gaps), `docs/BENCHMARK.md` (performance metrics), `CONTEXT.md` (domain glossary), `docs/superpowers/specs/` (approved design docs). This file is only an overview + roadmap.
>
> **Rearchitecture (modular monolith):** the Go server is moving to a prod-style layout (modular monolith + DI + composition root + multi-binary), swapping the stack to Gin + GORM + gormigrate + viper + zap while keeping the Python worker as the data plane. **Phase 1 ✅** (composition root `internal/app` + lazy DI `pkg/di` + viper config + zap logger); **Phase 2 ✅** (GORM v1.31 + gormigrate v2 data layer + repository interfaces; pgx/goose removed); **Phase 3 ✅** (HTTP layer on Gin v1.11 — handlers, middleware, SSE, auth); **Phase 4 ✅** (modularize — `internal/services/{iam,serving,usage,inference}` + `internal/infrastructure/*` relocation + neutral auth port; cross-service deps wired only in `internal/app`); **Phase 5 ✅** (multi-binary — `cmd/{server,worker,migrate,seed}` + `services.api`/`services.worker` role flags; the deployment worker runs headless and independently of the API node); **Phase 6a ✅** (transactional outbox — `outbox` table + `internal/infrastructure/outbox`; deployment events written in the same tx as the domain row, drained by a background publisher on the API node); **Phase 6b ✅** (cache-aside API keys; usage rollup `usage_events`→`usage_daily` — later reworked to a Postgres source-of-truth, see Key Decisions). All rearchitecture phases done. Design: [`docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`](docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md) · Phase 1 plan: [`docs/superpowers/plans/2026-09-11-phase1-composition-root-di.md`](docs/superpowers/plans/2026-09-11-phase1-composition-root-di.md) · Phase 2 plan: [`docs/superpowers/plans/2026-09-12-phase2-data-layer.md`](docs/superpowers/plans/2026-09-12-phase2-data-layer.md) · Phase 3 plan: [`docs/superpowers/plans/2026-09-12-phase3-http-gin.md`](docs/superpowers/plans/2026-09-12-phase3-http-gin.md) · Phase 4 plan: [`docs/superpowers/plans/2026-09-12-phase4-modularize.md`](docs/superpowers/plans/2026-09-12-phase4-modularize.md) · Phase 5 plan: [`docs/superpowers/plans/2026-09-12-phase5-multi-binary.md`](docs/superpowers/plans/2026-09-12-phase5-multi-binary.md) · Phase 6a plan: [`docs/superpowers/plans/2026-09-13-phase6-outbox.md`](docs/superpowers/plans/2026-09-13-phase6-outbox.md) · Phase 6b plan: [`docs/superpowers/plans/2026-09-13-phase6b-cache-lock-usage.md`](docs/superpowers/plans/2026-09-13-phase6b-cache-lock-usage.md).

## Behavioral Guidelines

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

## Architecture

```
Client (SSE/HTTP) → Go Server (main) → gRPC stream → Python Worker (inference)
                         │
                         ├── OpenAI adapter (/v1/chat/completions)
                         ├── Agentic loop (tool-use orchestration, max 10 iter)
                         ├── Session manager (in-memory, multi-user, 8K ctx)
                         ├── Chat history + usage APIs (/api/v1/sessions, /api/v1/usage)
                         ├── BatchScheduler (static batching: coalesce 100ms, batch ≤ 4)
                         ├── Tool executor (LocalToolExecutor, 4 built-in tools)
                         ├── Events bus (Kafka: serving.deployment.events)
                         ├── Deployment worker (async PENDING→READY via ServingRuntimeAdapter)
                         ├── Routing (model→deployment READY, tenant-scoped)
                         └── Rate limiter (Redis)
```

- **Go server**: HTTP handlers, SSE streaming, session management, agentic loop, tool execution, gRPC client, batch scheduler
- **Python worker**: Model loading (HuggingFace), hand-written BPE tokenizer, forward pass, sampling, gRPC server-streaming (single + batch)
- **Proto**: Shared gRPC contract between the two sides — 2 server-streaming services: `InferenceService.Generate` (single) and `BatchInferenceService.BatchGenerate` (batch)

## Project Structure

```
ai_factory/
├── proto/                       # Protobuf definitions (gRPC contract)
│   └── inference.proto          #   single + batch service, StopReason, SamplingParams
├── go-server/                   # Go module (main server)
│   ├── go.mod / go.sum
│   ├── configs/config.yaml      #   Default config (viper; AI_FACTORY_* env overrides)
│   ├── pkg/di/                  #   Lazy DI container + lifecycle (container.go, errors.go, lifecycle.go)
│   ├── pkg/response/            #   Shared JSON envelope helpers (WriteJSON/WriteAPIError/WriteOpenAIError)
│   ├── cmd/
│   │   ├── server/main.go       #   API node (flags → config → logger → container → app.Run)
│   │   ├── worker/main.go       #   Deployment worker node (headless; services.api=false)
│   │   ├── migrate/main.go      #   gormigrate runner (app.RunMigrate)
│   │   └── seed/main.go         #   seeder runner (app.RunSeed)
│   └── internal/
│       ├── app/                 #   Composition root: options.go, registry.go, app.go, runner.go, seeder.go, adapters.go
│       ├── config/              #   config.go (viper loader + flat Config fields)
│       ├── infrastructure/
│       │   ├── database/        #   GORM open + gormigrate runner (goose adoption)
│       │   ├── observability/   #   zap logger, Prometheus metrics, W3C traces
│       │   ├── circuitbreaker/  #   3-state breaker
│       │   ├── retry/           #   exponential backoff + jitter
│       │   ├── message/         #   event envelope + Kafka/memory bus
│       │   ├── outbox/          #   transactional outbox (record + store + publisher)
│       │   ├── cache/           #   Redis rate limiter + cache-aside KV (usage rollup is Postgres-only)
│       │   ├── inference/       #   gRPC client + batch scheduler + pb/ (codegen)
│       │   └── middleware/      #   global Gin chain + neutral auth port (Authenticator)
│       ├── migrations/          #   10 gormigrate Go migrations (was db/migrations/*.sql)
│       └── services/
│           ├── iam/             #   tenants, users, API keys, JWT/API-key auth + Authenticator
│           ├── serving/         #   catalog (models/templates), deployments, runtime adapter + worker
│           ├── usage/           #   quota + usage metering
│           └── inference/       #   chat/SSE, agentic loop, tools, durable sessions
├── python-worker/               # Python inference worker
│   ├── pyproject.toml
│   ├── benchmark.py             #   performance benchmark
│   ├── continuous_batching_demo.py
│   ├── tests/                   #   conftest.py, test_tokenizer.py (pytest)
│   └── worker/
│       ├── server.py            #   gRPC server entry (InferenceServicer + BatchInferenceServicer)
│       ├── engine.py            #   InferenceEngine (single, streaming) — transformers
│       ├── batch_engine.py      #   BatchEngine (batched model.generate)
│       ├── engines/             #   EngineBackend interface + registry (selected via --engine)
│       │   ├── base.py          #     EngineBackend (interface) + get_backend()
│       │   ├── transformers.py  #     TransformersBackend (Qwen2.5-Coder-7B)
│       │   └── llama/           #     LlamaBackend (Qwen3.5-9B GGUF) — server.py, client.py
│       ├── generate_proto.py    #   regenerate pb/ from proto
│       ├── pb/                  #   generated gRPC stubs
│       └── model/tokenizer/     #   bpe.py (BPETokenizer), byte_level.py (byte-encoder) — hand-written
├── (models → G:\models)         # GGUF + llama.cpp + HF cache — ngoài repo
├── ui/                          # Static test UI: chat.html, concepts.html (embedded HTML)
├── web/                         # NextJS UI (App Router): /login /chat /platform (Usage + API Keys) /infra /admin
├── docs/                        # ARCHITECTURE.md, BENCHMARK.md, superpowers/specs/
├── scripts/                     # setup.sh, setup.ps1
├── CONTEXT.md                   # Domain glossary
└── CLAUDE.md                    # This file
```

## Key Decisions

- **Model:** Qwen2.5-Coder-7B-Instruct (default, `--engine transformers`), 4-bit NF4 quant (bitsandbytes), `device_map="auto"`, RTX 3060 12GB (ungated, no HF login needed). Naming convention: Go sends `"model":"qwen-3b"` (transformers); the llama backend sends `"model":"qwen3.5-9b"` to llama-server. Old docs say "Qwen 2.5 3B" — the code has been running 7B all along.
- **Tokens to care about:** EOS `151645` (`<|im_end|>`), PAD `151643` (`<|endoftext|>`).
- **Tokenizer:** hand-written byte-level BPE (`worker/model/tokenizer/`), IDs match HF 100%, used for encode/decode/batch in both `engine.py` and `batch_engine.py`. HF `AutoTokenizer` is kept only for `apply_chat_template` (decision D1, spec `docs/superpowers/specs/2026-08-08-tokenizer-design.md`).
- **Context window:** 8K tokens; truncation logic in Go when `EstimatedTokens() > 90%` of the budget.
- **Batching:** `BatchScheduler` coalesces requests within a **100ms** window or up to **batch 4**, sends `BatchGenerate`; Go routes events by `request_id`. This is **static batching** (coalesced before a single forward pass), not dynamic/continuous batching.
- **Streaming:** gRPC server-streaming (Python→Go), SSE (Go→Client), streams each token immediately.
- **Protocol:** OpenAI `/v1/chat/completions` only. The dual protocol was collapsed to OpenAI-only on 2026-08-15 — the Messages API dialect, its adapter, and the UI protocol dropdown were removed to keep a single contract. Requests convert to the internal canonical format (`session.Message`).
- **Tools:** Interface `ToolExecutor` → `LocalToolExecutor` (4 tools: `read_file`, `write_file`, `run_command`, `list_files`; 30s timeout; `run_command` uses `sh -c` without a sandbox). The interface allows swapping in a sandbox later.
- **Agentic loop:** max `MaxToolIterations = 10`; tool results are not streamed back to the client; they are fed into the session for the next inference turn.
- **Error handling:** Cancel propagation from client → Go → gRPC → Python (100ms poll); tool errors first, the rest later.
- **Multi-user:** In-memory sessions distinguished by the `x-session-id` header (set by the client); no auth.
- **Routing + rate limit (M3):** `/v1/chat/completions` resolves request `model` (a `Model.name` in the registry) → the tenant's newest READY deployment; 404 `RESOURCE_NOT_FOUND` if none. Rate limit: Redis-backed (`AI_FACTORY_REDIS_ADDR`, default `localhost:6379`) — tenant RPM (`AI_FACTORY_RATE_LIMIT_RPM`, default 60) + deployment concurrency (`AI_FACTORY_RATE_LIMIT_CONCURRENCY`, default 4); fail-open on Redis down.
- **gRPC codegen:** Go uses `protoc-gen-go-grpc`, Python uses `grpcio-tools` (regenerated via `python -m worker.generate_proto`).
- **Chat history (sidebar):** sessions are durable (list/title/rename/delete via `/api/v1/sessions`), auto-titled from the first user message (40-rune truncate). The UI sidebar is ChatGPT-style on `/chat`.
- **Usage metering:** per-turn prompt/completion tokens are persisted to `usage_events` (best-effort, never fails a turn) and surfaced on `/platform` (Usage tab) + `/api/v1/usage`. Infra management (deployments/models/templates/quotas) moved to `/infra`.
- **UI usability (2026-09-13):** the API key is shown in full once with a `CopyButton`; users are created on `/admin` via `POST /api/v1/users` (+ `GET` list by tenant, gated `tenant.manage`); deployment creation uses cascading dropdowns (`GET /api/v1/models/:id/versions`, `GET /api/v1/templates/:id/versions`) instead of hand-typed UUIDs; the deployments table polls every 3s while a row is non-terminal; chat defaults to the registry model (prefers `qwen-3b`). The Dockerfile copies `configs/` into the image (the container previously exited with a missing `configs/config.yaml`). Deferred UX polish: 401 auto-logout, session id in the URL, message copy/regenerate, password prefill.
- **Composition root (Phase 1 ✅):** all wiring lives in `internal/app` (`registry.go` registers DI providers by name; `app.go` owns seed → worker → HTTP → graceful shutdown), dependencies are built lazily by `pkg/di`. Config is viper (`configs/config.yaml` + `AI_FACTORY_*` env overrides, loader `config.Load(path)`); logging is a zap core bridged into `slog` via `zapslog`.
- **Data layer (Phase 2 ✅):** GORM v1.31 + gormigrate v2 replace pgx + goose. `internal/infrastructure/database` owns `Open`/`Migrate` (with one-time adoption of an existing goose-migrated DB), `internal/migrations` holds the 10 Go migrations (table/column names unchanged), and data access goes through repository interfaces (`iam.Repositories`, `serving.Repositories`, `usage.Repositories`, `inference.Store`) so services never see `*gorm.DB`.
- **HTTP layer (Phase 3 ✅):** Gin v1.11 replaces `net/http` + `http.ServeMux`. Handlers take `*gin.Context` and respond via `c.JSON`; per-route auth middleware (`middleware.RequireAuth`/`RequirePermission`/`InferenceAuth`) are `gin.HandlerFunc`s; the global chain is recovery → CORS → trace → logging → metrics; `/metrics` is mounted with `gin.WrapH`. SSE keeps the existing `SSEWriter` driven by `c.Writer` (a `gin.ResponseWriter`, still `http.Flusher`).
- **Modularize (Phase 4 ✅):** the flat `internal/*` packages are gone. Services live in `internal/services/{iam,serving,usage,inference}` (each owning its handlers/router/repositories/models); generic infrastructure moved to `internal/infrastructure/*` (`message`, `cache`, `inference`, `middleware`, …); shared JSON envelopes in `pkg/response`. Auth is a neutral port: `infrastructure/middleware.Authenticator` is implemented by `services/iam`. **No service imports another service** — cross-service seams are consumer-defined interfaces (`inference.DeploymentResolver`, `inference.UsageRecorder`, the `Auth` port) wired only in `internal/app` (`app/adapters.go`). Remaining phase: outbox/cache-aside (Phase 6) — spec `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`.
- **Multi-binary (Phase 5 ✅):** one codebase, four binaries — `cmd/server` (API + in-process worker), `cmd/worker` (headless deployment worker, no HTTP), `cmd/migrate` (gormigrate `Up`), `cmd/seed` (admin + demo seed). Role selection is config: `services.api` / `services.worker` (`configs/config.yaml`, env `AI_FACTORY_SERVICES_API`/`AI_FACTORY_SERVICES_WORKER`), both default `true`. `cmd/worker` forces `api=false`; `RegisterAll` gates API-only providers (inference client/scheduler/loop, HTTP handlers) and the worker provider, and `App.Run` seeds/serves only for the enabled roles. `app.RunMigrate`/`app.RunSeed` are the thin composition-root runners. Docker image builds all four binaries; the compose `worker` service is behind a `worker` profile (does not start on plain `docker compose up`).
- **Transactional outbox (Phase 6a ✅):** migration `0008_outbox` + `internal/infrastructure/outbox` (`Record`/`Store`/`Publisher`). The user-triggered deployment events (`deployment_created`, `deployment_stop_requested`) are written in the **same `db.Transaction`** as the deployment row via `DeploymentRepository.CreateWithEvent` (`outbox.EnqueueTx`), so a committed deployment always has its event. A background `Publisher` (500 ms poll, batch 100, registered only for `services.api`) drains unpublished rows → event bus → stamps `published_at`; failures bump `attempts`/`last_error` and are retried (at-least-once, made safe by the worker's idempotent state-machine guard). The serving handler no longer publishes directly — it enqueues through the consumer-defined `serving.EventSink`. Phase 6 reliability patterns (outbox, cache-aside, usage rollup) are all in.
- **Cache-aside + usage rollup (Phase 6b ✅):** API-key auth is cache-aside: `iam.AuthService` caches `apikey:<sha256>` for 5 min in `cache.KV` (inactive/expired never cached, Redis errors fall back to Postgres) and `DeleteAPIKey` returns the `key_hash` so the handler invalidates it. Usage is **Postgres source-of-truth + derived rollup**: `RecordUsage` appends to `usage_events` (migration `0006_usage_events`), and a background `usage.Roller` (5 s loop, registered only for `services.api`) folds new events into `usage_daily` (migration `0009_usage_daily`) in one transaction — it locks the single watermark row (`usage_rollup_state`, migration `0011_usage_rollup_state`) with `SELECT ... FOR UPDATE` so concurrent API replicas serialise, and only advances `last_event_id` after the upserts commit (crash-safe, idempotent, no double count). Reads come from `usage_daily` (lag ≤ one interval). Migration `0010_backfill_usage_daily` folds pre-rollup history in once. (This replaced an earlier Redis-counter + `Flusher` design, which silently lost usage — and hid history — when Redis was unreachable.)
- **Load bounds (C1–C4 ✅):** the data plane is now bounded end to end. `BatchScheduler` holds a fixed number of **Batch Slots** (`DefaultMaxInFlightBatches = 1`): the collector blocks once the slot is taken, so `submitCh` (cap 100) fills and `TrySubmit` sheds load — the in-flight batch count is no longer unbounded. `dispatchBatch` derives its gRPC context from the requests (cancels once every request in the batch is gone or on shutdown) and closes **every** request channel on all exit paths (including a stream that ends without `final`), so a slow/aborted worker can't leak a goroutine. `--max-concurrent` now sets the batch size whenever `> 0` (default `0` = 4). The Python `EngineBackend` holds an **Engine Concurrency Guard** (shared `asyncio.Lock` wrapping `generate`/`generate_batch`) so the single and batch paths can never drive the same model at once, and `batch_engine` reads its token queue via `asyncio.to_thread` so it no longer blocks the event loop. `Manager` uses a **bounded in-process LRU** (default 10 000 sessions, eviction safe because Postgres is source-of-truth), coalesces concurrent loads per (session, owner) with `singleflight`, and does DB I/O outside its lock. Postgres runs on an explicit pool budget (`database.Open` with `PoolConfig`, defaults 25/25/30m/5m, `SkipDefaultTransaction: true`); the HTTP server sets `ReadHeaderTimeout`/`IdleTimeout` and leaves `WriteTimeout = 0` for SSE. Budgets live in `configs/config.yaml`.

## Engine selection

The worker supports 2 engines, chosen at startup (one model at a time, 12GB VRAM):

| Flag | Engine | Model | Runtime |
|---|---|---|---|
| `--engine transformers` (default) | TransformersBackend | Qwen2.5-Coder-7B (4-bit NF4) | transformers + bitsandbytes + BPETokenizer |
| `--engine llama` | LlamaBackend | Qwen3.5-9B (GGUF Q4_K_M) | llama-server (llama.cpp) + httpx proxy |

Details: `docs/superpowers/specs/2026-08-10-qwen35-gguf-engine-design.md`.

## Learning Roadmap

| Phase | Content | Status |
|---|---|---|
| Weeks 1–2 | E2E: proto → gRPC → Go → model; OpenAI protocol + SSE; agentic loop; static batching | ✅ Done |
| Weeks 3–4 | Hand-write byte-level BPE tokenizer | ✅ Done — spec approved, integrated into pipeline |
| Weeks 5–6 | Hand-write the sampling loop (greedy / temperature / top-p / top-k) | 🔜 Next — currently handled by HF `model.generate()` |
| Weeks 7–8 | Hand-manage KV cache + dynamic batching | 🔜 Not yet |
| Weeks 9+ | Hand-written forward pass, prefix caching, PagedAttention | 🔜 Not yet |

Code↔roadmap mapping details: `docs/ARCHITECTURE.md` §12.

## Known Gaps

- **Tool-calling is dead on transformers, works on llama** (`ARCHITECTURE.md` §9.1): on the `transformers` engine (Qwen2.5-Coder-7B), the batch path does not detect `tool_use` (only emits `STOP_END_TURN`/`STOP_MAX_TOKENS`) → the tool branch dies. On the `llama` engine (Qwen3.5-9B), tool-use **works and is verified E2E** — the model calls `read_file`, the Go loop executes, the model answers with the file's contents.
- **Client-provided tools not wired up** (§9.2): the loop always uses the 4 built-in tools of `LocalToolExecutor`; tools the client declares in the request are ignored.
- **Fixed:** the `--max-concurrent ≤ 1` batch-size override bug (default is now `0` = use `DefaultMaxBatchSize`; any `> 0` value sets the batch size).
- **Not yet:** persistence for other domains, sandbox for `run_command`, cost/quotas enforcement (usage is recorded but not yet enforced against quotas).
- **Auth trên inference đã có** (consumer slice): `/v1/chat/completions` yêu cầu `Authorization: Bearer <JWT hoặc API key>`; UI 3 trang login/chat/keys. Chi tiết `docs/superpowers/specs/2026-08-15-consumer-auth-ui-design.md`.

## Running

```bash
# Terminal 1: Python worker (default port 50051) — engine transformers (default, Qwen2.5-Coder-7B)
cd python-worker && python -m worker.server

#   ... or engine llama (Qwen3.5-9B GGUF): spawns llama-server on port 8081.
#   (add --llama-bin G:\models\llama.cpp\llama-server.exe if llama-server is not on PATH)
cd python-worker && python -m worker.server --engine llama --gguf G:\models\Qwen3.5-9B-Q4_K_M.gguf

# Terminal 2: Go server (default port 8080)
# NOTE: the server requires Postgres (control plane) and fails at boot if the DB is
# unreachable. Start it first if not already running:
#   docker compose -f deployments/docker-compose.yml up -d postgres kafka redis
# Kafka is optional (only the deployment worker needs it): if unreachable the server
# warns and runs with an in-memory event bus (deployments stay PENDING).
# Redis backs the rate limiter. On boot, seedDemo seeds model `qwen-3b` + a READY
# deployment for the demo tenant (best-effort; skip with AI_FACTORY_SKIP_SEED=1).
# The DB URL comes from AI_FACTORY_DATABASE_URL (default: local dev compose).
cd go-server && go run ./cmd/server/

# Terminal 2b (optional): run the deployment worker as its OWN process instead of
# in-process. Disable it on the server with AI_FACTORY_SERVICES_WORKER=false, then:
cd go-server && go run ./cmd/worker/     # headless (no HTTP); needs Kafka up
# Other Phase-5 binaries:
cd go-server && go run ./cmd/migrate/    # apply gormigrate migrations, then exit
cd go-server && go run ./cmd/seed/       # seed admin + demo tenant/model/deployment

# Terminal 3: NextJS UI (default port 3000) — proxies /api/v1 + /v1 + SSE to the Go server.
cd web && npm install && npm run dev

# Quick test (OpenAI adapter) — content is a plain STRING
# NOTE: inference endpoints now require auth. Login first, then pass the JWT (or an API key):
#   TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
#     -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'

# Health
curl http://localhost:8080/health
```

## Development

```bash
# Tests (tokenizer, CPU-only — requires an environment with transformers)
cd python-worker && python -m pytest tests/

# Regenerate Python gRPC stubs after editing proto/inference.proto
cd python-worker && python -m worker.generate_proto

# Go: regenerate pb/ with protoc + protoc-gen-go + protoc-gen-go-grpc
# (current versions recorded in the generated file header: protoc v5.29.3, protoc-gen-go v1.36.11, protoc-gen-go-grpc v1.6.2)

# M2 async-deploy demo (server + postgres + kafka up; requires jq)
bash scripts/m2-demo.sh
```
