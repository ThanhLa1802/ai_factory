# CONTEXT.md — AI Factory

Dự án học tập mô phỏng cách server Claude Code và ChatGPT hoạt động, bao gồm cả ba tầng: inference engine, agentic loop, và API server.

## Architecture

```
Client (SSE/HTTP) → Go Server (main)
    ├── HTTP Handler (OpenAI /v1/chat/completions)
    ├── Agentic Loop (tool-use orchestration, max 10 iterations)
    ├── Batch Scheduler (gom request 100ms, dispatch batch xuống Python)
    ├── Session Manager (in-memory, multi-user, context truncation 8K)
    └── gRPC Client ──► Python Worker
                            ├── InferenceService.Generate()  (single request, streaming token)
                            ├── BatchInferenceService.BatchGenerate() (batch, route per request_id)
                            └── EngineBackend (chọn bằng --engine)
                                 ├── TransformersBackend → InferenceEngine + BatchEngine (HF 4-bit, Qwen2.5-Coder-7B)
                                 └── LlamaBackend → llama-server proxy /v1/chat/completions (Qwen3.5-9B GGUF)
```

### Data Flow — Single Request
```
Handler → Agentic Loop → BatchScheduler.Submit() → collectorLoop (100ms window)
    → gRPC BatchGenerate (batch có thể chỉ có 1) → Python BatchEngine
    → model.generate(batch=[req]) → split output → stream token per request_id
    → Go route về đúng channel → Loop events → SSE/JSON response
```

### Data Flow — Concurrent Requests (Continuous Batching)
```
Handler 1 ──┐
Handler 2 ──┼──► BatchScheduler.submitCh ──► collectorLoop (gom 100ms)
Handler 3 ──┘                                     │
                                                  │ dispatch 1 batch [A,B,C]
                                                  ▼
                            Python BatchEngine.model.generate(batch=[A,B,C])
                            GPU xử lý 3 requests trong 1 forward pass
                                                  │
                                                  │ stream (req_id, token)
                                                  ▼
                            Go route per request_id → 3 channels riêng
                            → 3 SSE streams đồng thời
```

## Glossary

### Core Concepts

- **Inference Engine (Tầng A)**: Module Python chịu trách nhiệm load model, tokenize, chạy forward pass, và sinh token. Chạy như một worker riêng biệt, giao tiếp với Go server qua gRPC. Có hai chế độ: `InferenceEngine.generate()` cho single request (streaming token), và `BatchEngine.generate_batch()` cho batch requests.
- **Agentic Loop (Tầng B)**: Vòng lặp multi-turn orchestrated trong Go server: nhận user message → gửi xuống inference qua BatchScheduler → model trả `tool_use` → execute tool → gửi `tool_result` lại model → lặp đến khi model trả `stop_reason: "end_turn"` hoặc đạt max iterations (10). ⚠️ Lưu ý: nhánh tool-use **hoạt động thật trên engine llama** (Qwen3.5-9B — llama-server tool calling native, verified E2E), nhưng **chết trên engine transformers** (Qwen2.5-Coder-7B): `TransformersBackend`/`BatchEngine` không phát hiện `tool_use` (chỉ sinh `STOP_END_TURN`/`STOP_MAX_TOKENS`). Xem `docs/ARCHITECTURE.md` §9.1.
- **API Server (Tầng C)**: HTTP server trong Go, expose OpenAI Chat Completions API (`/v1/chat/completions`), hỗ trợ SSE streaming.
- **Internal Canonical Format**: Định dạng message trung gian trong Go, dùng chung cho pipeline. Adapter layer chuyển đổi request OpenAI sang internal format trước khi xử lý.
- **gRPC Inference Service**: Contract giữa Go server và Python worker. `InferenceService.Generate` cho single request streaming. `BatchInferenceService.BatchGenerate` cho batch requests — nhận nhiều request, stream kết quả kèm `request_id` để route.
- **Batch Scheduler**: Go module (`BatchScheduler`) thay thế inference queue tuần tự. Gom các request đến trong cửa sổ 100ms thành batch → dispatch qua gRPC BatchGenerate → route token/event về đúng channel dựa trên `request_id`. Hỗ trợ max batch size configurable (default: 4).
- **Own Tokenizer**: Byte-level BPE tokenizer tự viết (`worker/model/tokenizer/`) thay thế HF `AutoTokenizer` trong pipeline — **đã hoàn thành ở Tuần 3-4**. Load `vocab.json` + `merges.txt` + `tokenizer_config.json` có sẵn của Qwen (IDs khớp 100%, không retrain) và tự viết: byte-encoder, regex pre-tokenization, BPE merge, decode (kể cả decode tăng dần cho streaming), batch pad/truncate. Có test đối chiếu ID == HF (`tests/test_tokenizer.py`). Chat template (Jinja) vẫn dùng `apply_chat_template` của HF — template ≠ tokenization (quyết định D1, xem spec `docs/superpowers/specs/2026-08-08-tokenizer-design.md`).
- **Continuous Batching**: Kỹ thuật gom nhiều inference requests vào cùng một GPU forward pass. Thay vì request A chạy xong mới đến request B, cả A, B, C cùng chạy trong một batch — tận dụng GPU compute và giảm latency cho request đến sau. Implementation hiện tại: static batch (batch cố định sau khi dispatch), chưa hỗ trợ dynamic add/remove giữa các decode step (sẽ có ở Tuần 7-8 khi tự quản lý KV cache).
- **Tool Executor**: Interface trong Go (`ToolExecutor`) định nghĩa cách execute tool. `LocalToolExecutor` là implementation đầu tiên, chạy tool trực tiếp trên host với timeout 30s. Có 4 built-in tools: `read_file`, `write_file`, `run_command`, `list_files`. Có thể swap sang sandbox implementation sau.
- **Session**: Đại diện cho một phiên trò chuyện của một user, chứa conversation history, context window (8K tokens), và trạng thái hiện tại. Lưu in-memory. Concurrency được quản lý bởi BatchScheduler thay vì inference queue.
- **Context Truncation**: Logic trong Go cắt bớt messages cũ nhất khi tổng số token vượt quá 8K, đảm bảo không cắt giữa cặp `tool_use`/`tool_result`.
- **Cancel Propagation**: Chain từ client disconnect → Go `ctx.Done()` → gRPC stream cancel → Python dừng inference → giải phóng VRAM. Với batch mode, cancel từng request riêng không ảnh hưởng các request khác trong batch. ⚠️ Giới hạn hiện tại: phía Python là poll **100ms** (không phải event-driven), và trong batch mode model vẫn chạy hết forward pass của batch — chỉ bỏ gửi kết quả của request bị cancel.
- **Engine Backend**: Interface trong Python worker (`EngineBackend`) tách inference engine khỏi gRPC servicers. Hai implementation: `TransformersBackend` (Qwen2.5-Coder-7B, transformers) và `LlamaBackend` (Qwen3.5-9B, llama-server proxy). Chọn bằng `--engine` lúc khởi động — mô hình "swap engine sau interface".
- **LlamaProxyEngine**: `LlamaBackend` — spawn `llama-server` subprocess, proxy gRPC → OpenAI-compatible `/v1/chat/completions` (SSE). Tool calling native (structured output) → nhánh tool-use của agentic loop hoạt động thật trên engine này (vá §9.1).

### Technology Decisions

- **Model**: Default Qwen2.5-Coder-7B-Instruct (transformers, 4-bit quantized via bitsandbytes NF4), chạy trên RTX 3060 12GB VRAM. Ungated — không cần HuggingFace login. Tùy chọn Qwen3.5-9B (GGUF Q4_K_M) qua engine llama. Docs cũ ghi "Qwen 2.5 3B" — code chạy 7B từ trước.
- **Inference Runtime**: HuggingFace `transformers` + `bitsandbytes` cho quantization (engine transformers). Batch mode dùng `model.generate()` với batched inputs (padding + attention mask). Engine llama dùng `llama-server` (llama.cpp) proxy qua OpenAI-compatible `/v1/chat/completions`.
- **Protocol**: gRPC server-streaming (Python → Go), SSE (Go → Client). Hai gRPC services: `InferenceService.Generate` (single) + `BatchInferenceService.BatchGenerate` (batch).
- **Batching Strategy**: Static batch — gom request trong cửa sổ 100ms, dispatch batch cố định qua gRPC. Python dùng `model.generate()` batched, decode từng token riêng lẻ, yield kèm `request_id`. Go scheduler route về channel per-request.
- **Languages**: Go (HTTP server, agentic loop, batch scheduler, session management), Python (inference worker, batch engine)
- **Architecture**: Monorepo phẳng, Go là main server, Python là inference worker sidecar

### Learning Roadmap

| Giai đoạn | Nội dung | Trạng thái |
|-----------|----------|------------|
| Tuần 1-2 | Dùng HuggingFace sẵn, focus end-to-end: proto → gRPC → Go server → model chạy | ✅ Hoàn thành |
| Tuần 1-2 | OpenAI protocol (`/v1/chat/completions`), SSE streaming, multi-turn agentic loop | ✅ Hoàn thành |
| Tuần 1-2 | Continuous Batching (static): BatchScheduler + BatchEngine + batch gRPC | ✅ Hoàn thành |
| Tuần 3-4 | Tự implement tokenizer (BPE encode/decode) | ✅ Hoàn thành |
| Tuần 5-6 | Tự implement sampling (greedy, temperature, top-p, top-k) | 🔜 Kế tiếp |
| Tuần 7-8 | Tự quản lý KV cache + dynamic continuous batching (add/remove giữa decode step) | 🔜 Chưa |
| Tuần 9+ | (Optional) Tự viết forward pass, prefix caching, PagedAttention | 🔜 Chưa |

Map chi tiết code ↔ roadmap: `docs/ARCHITECTURE.md` §12. Lỗ hổng tích hợp hiện tại (tool-calling chết trên transformers nhưng chạy trên llama, tool client chưa nối, bug `--max-concurrent`): `docs/ARCHITECTURE.md` §9.
