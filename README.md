# AI Factory

A learning project that simulates a **Claude Code / ChatGPT**-style server: an inference engine (Python) wired to an agentic Go API server over gRPC, speaking the OpenAI protocol.

> 🔭 **Where is the project now?** See [Current Status](#current-status) and the detailed Vietnamese tracker [`docs/TRACKING.md`](docs/TRACKING.md).

## Features

- **OpenAI protocol** — `/v1/chat/completions`, normalized into one internal message format
- **SSE streaming** — tokens stream Python → Go → client in real time
- **Agentic loop** — tool-use orchestration (max 10 iterations), 4 built-in tools: `read_file`, `write_file`, `run_command`, `list_files`
- **Static batching** — requests coalesced in a 100 ms window (batch ≤ 4), routed back by `request_id`
- **Two inference engines** — switchable at startup via `--engine`
- **Hand-written byte-level BPE tokenizer** — IDs match HuggingFace 100%
- **Multi-user** — in-memory sessions keyed by `x-session-id`

## Architecture

```
Client (SSE/HTTP) → Go Server (main) → gRPC stream → Python Worker (inference)
                         │
                         ├── OpenAI adapter (/v1/chat/completions)
                         ├── Agentic loop (tool-use orchestration, max 10 iter)
                         ├── Session manager (in-memory, multi-user, 8K ctx)
                         ├── BatchScheduler (static batching: coalesce 100ms, batch ≤ 4)
                         └── Tool executor (LocalToolExecutor, 4 built-in tools)
```

## Engines

| Flag | Engine | Model | Runtime |
|---|---|---|---|
| `--engine transformers` (default) | TransformersBackend | Qwen2.5-Coder-7B (4-bit NF4) | transformers + bitsandbytes + BPETokenizer |
| `--engine llama` | LlamaBackend | Qwen3.5-9B (GGUF Q4_K_M) | llama-server (llama.cpp) + httpx proxy |

Tool-calling currently works end-to-end only on the **llama** engine (§9.1 in [ARCHITECTURE](docs/ARCHITECTURE.md)).

## Tech Stack

| Layer | Tech |
|---|---|
| API server | Go (net/http, SSE) |
| Inference worker | Python — HuggingFace transformers, bitsandbytes, llama.cpp (llama-server) |
| Contract | gRPC — [`proto/inference.proto`](proto/inference.proto) (2 server-streaming services) |
| Tokenizer | Hand-written byte-level BPE (`python-worker/worker/model/tokenizer/`) |

## Quick Start

```bash
# Terminal 1: Python worker (default port 50051) — engine transformers (default, Qwen2.5-Coder-7B)
cd python-worker && python -m worker.server

#   ... or engine llama (Qwen3.5-9B GGUF): spawns llama-server on port 8081.
#   (add --llama-bin G:\models\llama.cpp\llama-server.exe if llama-server is not on PATH)
cd python-worker && python -m worker.server --engine llama --gguf G:\models\Qwen3.5-9B-Q4_K_M.gguf

# Terminal 2: Go server (default port 8080)
# NOTE: the server requires Postgres (control plane) and fails at boot if the DB is
# unreachable. Start it first if not already running:
#   docker compose -f deployments/docker-compose.yml up -d postgres
# The DB URL comes from AI_FACTORY_DATABASE_URL (default: local dev compose).
cd go-server && go run ./cmd/server/

# Quick test (OpenAI adapter) — content is a plain STRING
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'

# Health
curl http://localhost:8080/health
```

## Project Structure

```
ai_factory/
├── proto/            # Protobuf definitions (gRPC contract)
├── go-server/        # Go module — HTTP, SSE, agentic loop, batch scheduler, gRPC client
├── python-worker/    # Python worker — engines, hand-written tokenizer, gRPC server
├── (models → G:\models)  # GGUF + llama.cpp + HF cache — ngoài repo
├── ui/               # Static test UI: chat.html, concepts.html
├── docs/             # ARCHITECTURE.md, BENCHMARK.md, TRACKING.md, specs/
├── scripts/          # setup.sh, setup.ps1
└── CONTEXT.md        # Domain glossary
```

## Documentation

| Doc | Description |
|---|---|
| [CLAUDE.md](CLAUDE.md) | Project overview, key decisions, engine selection, roadmap (EN) |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Detailed component deep-dive + integration gaps (VI) |
| [docs/TRACKING.md](docs/TRACKING.md) | Progress tracker — what's done, what's next (VI) |
| [docs/BENCHMARK.md](docs/BENCHMARK.md) | Performance numbers, KV cache memory model (VI) |
| [CONTEXT.md](CONTEXT.md) | Domain glossary |

## Current Status

| Phase | Content | Status |
|---|---|---|
| Weeks 1–2 | E2E pipeline, OpenAI protocol, SSE, agentic loop, static batching | ✅ Done |
| Weeks 3–4 | Hand-written byte-level BPE tokenizer | ✅ Done |
| Bonus | Llama engine (Qwen3.5-9B GGUF) + working tool-calling | ✅ Done |
| Weeks 5–6 | Hand-written sampling loop (greedy / temperature / top-p / top-k) | 🔜 Next — currently HF `model.generate()` |
| Weeks 7–8 | KV cache management + dynamic batching | 🔜 Not started |
| Weeks 9+ | Hand-written forward pass, prefix caching, PagedAttention | 🔜 Not started |

**Currently focused on:** the **sampling loop** (roadmap Weeks 5–6) — replacing `model.generate()` parameters with a hand-written sampler.

Detailed checklist, per-phase breakdown, and open gaps: [`docs/TRACKING.md`](docs/TRACKING.md).
