# CLAUDE.md — AI Factory

Dự án học tập mô phỏng server Claude Code / ChatGPT, gồm inference engine, agentic loop, và API server.

> **Đọc thêm:** `docs/ARCHITECTURE.md` (kiến trúc chi tiết, deep-dive từng thành phần + lỗ hổng tích hợp), `docs/BENCHMARK.md` (số liệu hiệu năng), `CONTEXT.md` (glossary domain), `docs/superpowers/specs/` (design docs đã duyệt). File này chỉ là tổng quan + lộ trình.

## Architecture

```
Client (SSE/HTTP) → Go Server (main) → gRPC stream → Python Worker (inference)
                         │
                         ├── Anthropic adapter (/v1/messages)
                         ├── OpenAI adapter (/v1/chat/completions)
                         ├── Agentic loop (tool-use orchestration, max 10 iter)
                         ├── Session manager (in-memory, multi-user, 8K ctx)
                         ├── BatchScheduler (static batching: gom 100ms, batch ≤ 4)
                         └── Tool executor (LocalToolExecutor, 4 built-in tools)
```

- **Go server**: HTTP handlers, SSE streaming, session management, agentic loop, tool execution, gRPC client, batch scheduler
- **Python worker**: Model loading (HuggingFace), tokenizer BPE tự viết, forward pass, sampling, gRPC server-streaming (single + batch)
- **Proto**: Shared gRPC contract giữa hai bên — 2 service server-streaming: `InferenceService.Generate` (single) và `BatchInferenceService.BatchGenerate` (batch)

## Project Structure

```
ai_factory/
├── proto/                       # Protobuf definitions (gRPC contract)
│   └── inference.proto          #   single + batch service, StopReason, SamplingParams
├── go-server/                   # Go module (main server)
│   ├── go.mod / go.sum
│   ├── cmd/server/main.go       #   Entry point (flags: --port, --inference-addr, --max-concurrent)
│   └── internal/
│       ├── api/                 #   handler.go, adapters.go, sse.go (HTTP + dual protocol + SSE)
│       ├── agent/               #   loop.go (agentic loop), tools.go (ToolExecutor)
│       ├── session/             #   session.go, manager.go (in-memory, truncation)
│       └── inference/           #   client.go (gRPC), batch_scheduler.go, pb/ (codegen)
├── python-worker/               # Python inference worker
│   ├── pyproject.toml
│   ├── benchmark.py             #   benchmark số liệu hiệu năng
│   ├── continuous_batching_demo.py
│   ├── tests/                   #   conftest.py, test_tokenizer.py (pytest)
│   └── worker/
│       ├── server.py            #   gRPC server entry (InferenceServicer + BatchInferenceServicer)
│       ├── engine.py            #   InferenceEngine (single, streaming)
│       ├── batch_engine.py      #   BatchEngine (batched model.generate)
│       ├── generate_proto.py    #   sinh lại pb/ từ proto
│       ├── pb/                  #   generated gRPC stubs
│       └── model/tokenizer/     #   bpe.py (BPETokenizer), byte_level.py (byte-encoder) — tự viết
├── ui/                          # Static test UI: chat.html, concepts.html (HTML nhúng)
├── docs/                        # ARCHITECTURE.md, BENCHMARK.md, superpowers/specs/
├── scripts/                     # setup.sh, setup.ps1
├── CONTEXT.md                   # Domain glossary
└── CLAUDE.md                    # This file
```

## Key Decisions

- **Model:** Qwen 2.5 3B Instruct, quant 4-bit NF4 (bitsandbytes), `device_map="auto"`, RTX 3060 12GB (ungated, không cần HF login). Naming convention: `"model":"qwen-3b"`.
- **Token cần quan tâm:** EOS `151645` (`<|im_end|>`), PAD `151643` (`<|endoftext|>`).
- **Tokenizer:** tự viết byte-level BPE (`worker/model/tokenizer/`), IDs khớp 100% với HF, dùng cho encode/decode/batch trong cả `engine.py` lẫn `batch_engine.py`. HF `AutoTokenizer` chỉ giữ để `apply_chat_template` (quyết định D1, spec `docs/superpowers/specs/2026-08-08-tokenizer-design.md`).
- **Context window:** 8K tokens, truncate logic ở Go khi `EstimatedTokens() > 90%` budget.
- **Batching:** `BatchScheduler` gom request trong window **100ms** hoặc tới **batch 4**, gửi `BatchGenerate`; Go route event theo `request_id`. Đây là **static batch** (gom trước 1 forward pass), không phải dynamic/continuous batching.
- **Streaming:** gRPC server-streaming (Python→Go), SSE (Go→Client), gửi từng token ngay.
- **Dual protocol:** Anthropic `/v1/messages` + OpenAI `/v1/chat/completions` → chuyển về internal canonical format (`session.Message`).
- **Tools:** Interface `ToolExecutor` → `LocalToolExecutor` (4 tools: `read_file`, `write_file`, `run_command`, `list_files`; timeout 30s; `run_command` dùng `sh -c` không sandbox). Interface cho phép swap sandbox sau.
- **Agentic loop:** max `MaxToolIterations = 10`; tool results không stream về client, được đưa vào session cho lượt inference kế.
- **Error handling:** Cancel propagation từ client → Go → gRPC → Python (poll 100ms) + tool error trước, còn lại để sau.
- **Multi-user:** In-memory sessions phân biệt bằng header `x-session-id` (client tự đặt); không auth.
- **gRPC codegen:** Go dùng `protoc-gen-go-grpc`, Python dùng `grpcio-tools` (sinh lại bằng `python -m worker.generate_proto`).

## Lộ trình học tập (learning roadmap)

| Giai đoạn | Nội dung | Trạng thái |
|---|---|---|
| Tuần 1–2 | E2E: proto → gRPC → Go → model; dual protocol + SSE; agentic loop; static batching | ✅ Xong |
| Tuần 3–4 | Tự viết tokenizer byte-level BPE | ✅ Xong — spec đã duyệt, tích hợp vào pipeline |
| Tuần 5–6 | Tự viết sampling loop (greedy / temperature / top-p / top-k) | 🔜 Kế tiếp — hiện do HF `model.generate()` đảm nhiệm |
| Tuần 7–8 | Tự quản lý KV cache + dynamic batching | 🔜 Chưa |
| Tuần 9+ | Forward pass tự viết, prefix caching, PagedAttention | 🔜 Chưa |

Chi tiết map code ↔ roadmap: `docs/ARCHITECTURE.md` §12.

## Known Gaps / Lỗ hổng hiện tại

- **Tool-calling chết trong đường batch** (`ARCHITECTURE.md` §9.1): agentic loop chỉ dùng `BatchGenerate`, nhưng `BatchEngine` không phát hiện `tool_use` (chỉ sinh `STOP_END_TURN`/`STOP_MAX_TOKENS`) → nhánh tool chưa kích hoạt ở runtime.
- **Tool từ client chưa nối** (§9.2): loop luôn dùng 4 built-in tools của `LocalToolExecutor`, tool client khai báo trong request bị bỏ qua.
- **Bug nhỏ** (§9.4): flag `--max-concurrent ≤ 1` không ghi đè batch size; log `max_batch` sai khi flag = 1.
- **Chưa có:** auth/rate-limit/persistence, sandbox cho `run_command`, observability (metrics/tracing/cost). Không phải repo git trong working copy hiện tại.

## Running

```bash
# Terminal 1: Python worker (mặc định port 50051)
cd python-worker && python -m worker.server

# Terminal 2: Go server (mặc định port 8080)
cd go-server && go run ./cmd/server/

# Test nhanh (Anthropic adapter)
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'

# Health
curl http://localhost:8080/health
```

## Development

```bash
# Test (tokenizer, CPU-only — cần môi trường có transformers)
cd python-worker && python -m pytest tests/

# Sinh lại gRPC stubs Python sau khi sửa proto/inference.proto
cd python-worker && python -m worker.generate_proto

# Go: sinh lại pb/ bằng protoc + protoc-gen-go + protoc-gen-go-grpc
# (phiên bản hiện tại ghi trong header file sinh: protoc v5.29.3, protoc-gen-go v1.36.11, protoc-gen-go-grpc v1.6.2)
```
