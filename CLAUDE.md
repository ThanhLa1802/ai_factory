# CLAUDE.md — AI Factory

A learning project simulating a Claude Code / ChatGPT server, comprising an inference engine, an agentic loop, and an API server.

> **Further reading:** [`README.md`](README.md) (public overview), [`docs/TRACKING.md`](docs/TRACKING.md) (progress tracker — where the project currently is), `docs/ARCHITECTURE.md` (detailed architecture, deep-dive into each component + integration gaps), `docs/BENCHMARK.md` (performance metrics), `CONTEXT.md` (domain glossary), `docs/superpowers/specs/` (approved design docs). This file is only an overview + roadmap.

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
│   ├── cmd/server/main.go       #   Entry point (flags: --port, --inference-addr, --max-concurrent)
│   └── internal/
│       ├── api/                 #   handler.go, adapters.go, sse.go (HTTP + OpenAI protocol + SSE)
│       ├── agent/               #   loop.go (agentic loop), tools.go (ToolExecutor)
│       ├── session/             #   session.go, manager.go (in-memory, truncation)
│       └── inference/           #   client.go (gRPC), batch_scheduler.go, pb/ (codegen)
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
├── models/                      # GGUF + llama.cpp: Qwen3.5-9B-Q4_K_M.gguf, llama.cpp/llama-server.exe
├── ui/                          # Static test UI: chat.html, concepts.html (embedded HTML)
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
- **Minor bug** (§9.4): the `--max-concurrent ≤ 1` flag does not override the batch size; `max_batch` is logged incorrectly when the flag = 1.
- **Not yet:** persistence, sandbox for `run_command`, observability (usage/tracing/cost).
- **Auth trên inference đã có** (consumer slice): `/v1/chat/completions` yêu cầu `Authorization: Bearer <JWT hoặc API key>`; UI 3 trang login/chat/keys. Chi tiết `docs/superpowers/specs/2026-08-15-consumer-auth-ui-design.md`.

## Running

```bash
# Terminal 1: Python worker (default port 50051) — engine transformers (default, Qwen2.5-Coder-7B)
cd python-worker && python -m worker.server

#   ... or engine llama (Qwen3.5-9B GGUF): spawns llama-server on port 8081.
#   (add --llama-bin ..\models\llama.cpp\llama-server.exe if llama-server is not on PATH)
cd python-worker && python -m worker.server --engine llama --gguf ..\models\Qwen3.5-9B-Q4_K_M.gguf

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
