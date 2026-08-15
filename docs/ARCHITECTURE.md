# AI Factory — Kiến trúc chi tiết

> Tài liệu này mô tả kiến trúc **thực tế** của dự án dựa trên mã nguồn hiện tại, bổ sung cho `CONTEXT.md` (glossary ngắn) và `CLAUDE.md` (tổng quan + lộ trình). Nó đi sâu vào từng thành phần, luồng dữ liệu và các giới hạn tích hợp.

**Trạng thái doc:** khớp với code tại commit hiện tại (2026-08-10, sau khi thêm engine llama). Nếu có thay đổi kiến trúc, hãy cập nhật lại.

---

## 1. Tổng quan

AI Factory là dự án học tập mô phỏng backend của Claude Code / ChatGPT, gồm **ba tầng**:

| Tầng | Ngôn ngữ | Vai trò |
|---|---|---|
| **API Server** | Go | HTTP/SSE, OpenAI Chat Completions (`/v1/chat/completions`), session, agentic loop, tool executor |
| **Inference Worker** | Python | Load model (HuggingFace), tokenize, forward pass, sampling, batch inference |
| **Contract** | Protobuf | gRPC server-streaming giữa Go và Python |

Mô hình triển khai là **monorepo phẳng, Go = main server, Python = sidecar worker**, giao tiếp qua gRPC trên localhost. Kiến trúc này tương đồng với production (API gateway + vLLM/TensorRT-LLM backend) ở dạng thu nhỏ.

### 1.1 Sơ đồ tổng thể

```
                        ┌─────────────────────────────────────────────────┐
                        │                  GO SERVER                       │
                        │  (localhost:8080)                                │
 Client                │                                                 │
(SSE / HTTP)  ───────► │  ┌─────────────────────────────────────────────┐ │
                        │  │ api.Handler (internal/api)                  │ │
                        │  │  ├─ /v1/chat/completions  (OpenAI)          │ │
                        │  │  ├─ /health  /v1/sessions/{id}  /  /concepts│ │
                        │  │  └─ adapters.go  ──► internal canonical     │ │
                        │  └───────────────┬─────────────────────────────┘ │
                        │                  │ LoopEvent stream (chan)       │
                        │  ┌───────────────▼─────────────────────────────┐ │
                        │  │ agent.Loop (agentic loop, max 10 iter)     │ │
                        │  │  ├── session.Manager (in-memory, 8K ctx)    │ │
                        │  │  └── ToolExecutor → LocalToolExecutor       │ │
                        │  └───────────────┬─────────────────────────────┘ │
                        │                  │ inference.GenerateRequest     │
                        │  ┌───────────────▼─────────────────────────────┐ │
                        │  │ inference.BatchScheduler (100ms window)     │ │
                        │  │  collectorLoop → gRPC BatchGenerate          │ │
                        │  │  route event theo request_id                  │ │
                        │  └───────────────┬─────────────────────────────┘ │
                        │                  │ gRPC (localhost:50051)         │
                        └──────────────────┼──────────────────────────────┘
                                           ▼
                        ┌─────────────────────────────────────────────────┐
                        │              PYTHON WORKER                       │
                        │  InferenceServicer + BatchInferenceServicer     │
                        │  └── EngineBackend (chọn bằng --engine)          │
                        │      ├── TransformersBackend                     │
                        │      │    ├─ InferenceEngine (single, streaming) │
                        │      │    └─ BatchEngine (batched generate)      │
                        │      │    Model: Qwen2.5-Coder-7B (4-bit NF4)    │
                        │      └── LlamaBackend (Qwen3.5-9B GGUF)          │
                        │           spawn llama-server → /v1/chat/...      │
                        └─────────────────────────────────────────────────┘
```

### 1.2 Hình dạng request trong hệ thống

Một request trải qua **3 lần "đổi format"**:

1. **Protocol gốc** (`OpenAIRequest`) → `adapters.go` chuyển về **internal canonical format** (`session.Message`).
2. Internal → **proto** (`inference.pb.go`) tại `internal/inference/client.go` / `batch_scheduler.go`.
3. Proto → **dict OpenAI-style** tại Python (`server.py` `_messages_from_proto` / `_tools_from_proto`) → chat template của model.

Cấu trúc message internal (protocol-agnostic) nằm ở `internal/session/session.go`:

```go
type Message struct {
    Role        string     // "user" | "assistant" | "system" | "tool"
    Content     string
    ToolCalls   []ToolCall // assistant gọi tool
    ToolCallID  string     // tool_result trỏ tới tool_use nào
    ToolResult  string
    IsError     bool
}
```

---

## 2. Thành phần Go Server

Entry point: `go-server/cmd/server/main.go`. Các flag:

| Flag | Mặc định | Ý nghĩa |
|---|---|---|
| `--port` | `8080` | Cổng HTTP |
| `--inference-addr` | `localhost:50051` | Địa chỉ gRPC Python worker |
| `--workdir` | `.` | Thư mục làm việc cho tool execution |
| `--max-concurrent` | `1` | Batch size (chỉ override nếu `>1`) |

**Dependency wiring** (`main.go:32-59`): gRPC client → BatchScheduler → Loop → SessionManager → HTTP handler. Lưu ý: SessionManager **không còn quản lý queue inference** — việc đó do BatchScheduler đảm nhiệm (bình luận trong code).

### 2.1 HTTP / API layer — `internal/api/`

Routes đăng ký ở `handler.go:32-42`:

| Route | Method | Chức năng |
|---|---|---|
| `/v1/chat/completions` | POST | OpenAI Chat Completions (stream/non-stream) |
| `/health` | GET | Health check JSON |
| `/v1/sessions/{id}` | GET/DELETE | Đọc / xoá session |
| `/` , `/ui` | GET | UI test tĩnh (HTML nhúng) |
| `/concepts` | GET | Tài liệu technical concepts (HTML nhúng) |

**Xử lý request chung** (`handler.go`, OpenAI `/v1/chat/completions`):

1. Decode + validate body.
2. Lấy session ID từ header `x-session-id`, nếu rỗng sinh UUID mới. Đây là cách **multi-user** được phân biệt — không có auth, session hoàn toàn dựa trên header client tự đặt.
3. `OpenAIToInternal` → internal messages + system prompt. System prompt được lưu vào session (`sess.SetSystemPrompt`), **không** đưa vào message history.
4. Cài `context.WithCancel(r.Context())` + goroutine chờ `r.Context().Done()` → đây là khâu đầu của **cancel propagation** (xem §6).
5. Nhánh `Stream=true` → SSE; ngược lại gom events thành JSON response.

**Adapters** (`adapters.go`):

- `OpenAIToInternal`: system message → system prompt; `tool_calls`/`tool_call_id` map trực tiếp.
- `ToolsToInternal` / `OpenAIToolsToInternal`: chuyển tool definitions client gửi lên. ⚠️ **Hai hàm này hiện không được handler gọi** — xem §9.2.

**SSE writer** (`sse.go`): headers `text/event-stream`, `no-cache`, `keep-alive`, `X-Accel-Buffering: no` (chống buffer của nginx). Gửi token ngay mỗi lần + `Flush()`.

- Định dạng OpenAI stream: `chat.completion.chunk` với `delta.content` / `delta.tool_calls`, rồi chunk `finish_reason`, rồi `[DONE]`.

### 2.2 Session Manager — `internal/session/`

- `Manager` = `map[string]*Session` + RWMutex (`manager.go:19-23`). `GetOrCreate`, `Get`, `Delete`, `List`.
- `Session` lưu: message history, `MaxTokens` (mặc định **8192**), `SystemPrompt`, timestamps (`session.go:41-55`).
- `EstimatedTokens()` (`session.go:99-113`): heuristic **chars/4** — chỉ là ước lượng nhanh phía Go; con số chính xác do Python worker cung cấp qua `Usage`.

**Context truncation** — `manager.go:77-137` `TruncateMessages`:

- Được trigger trong `loop.go:112-114` khi `EstimatedTokens() > 90% * MaxTokens` (`DangerZoneBeforeTruncate = 0.90`).
- Quét từ cuối về đầu, giữ những message gần nhất vừa token budget.
- **Bảo toàn cặp tool**: nếu message giữ đầu tiên là `tool_result`, lùi thêm để giữ luôn `tool_use` tương ứng. Nếu message cuối là `tool_use` không có kết quả (orphaned), bỏ nó đi — vì model không xử lý được.

### 2.3 Agentic Loop — `internal/agent/loop.go`

`Loop` giữ 2 dependency: `BatchScheduler` (inference) + `ToolExecutor` (tools). API chính: `RunStreaming(ctx, sess, userMessage, params) <-chan LoopEvent` — trả channel events (buffer 64), đóng khi xong.

`LoopEvent` có 5 loại (`loop.go:26-32`): `Token`, `ToolUse`, `ToolResult`, `Final`, `Error`.

**Vòng lặp chính** (`loop.go:97-239`), tối đa `MaxToolIterations = 10`:

```
1. Check ctx.Done() → nếu cancel, emit Final(STOP_CANCELLED)
2. sess.GetMessages() → truncate nếu vượt 90% budget
3. Lấy tool definitions từ executor.ListTools()
4. Build GenerateRequest → scheduler.Submit(ctx, req)
5. Tiêu thụ events từ gRPC:
     token     → forward ngay (LoopEventToken)
     tool_use  → lưu + emit (LoopEventToolUse)
     final     → đọc stop_reason; STOP_ERROR → emit LoopEventError, return
6. Lưu assistant message vào session (kèm tool_calls nếu có)
7. Nếu stop_reason == STOP_TOOL_USE và có toolCalls:
     → với mỗi tool: executor.Execute(ctx, name, args)
     → lưu tool message (Role=tool, ToolResult, IsError) vào session
     → emit LoopEventToolResult
     → continue (vòng mới — model nhìn thấy tool results)
8. Ngược lại: emit LoopEventFinal(stop_reason, usage), return
```

Tool results **không** stream về client — chúng được đưa vào session và đi vào lượt inference tiếp theo (handler có comment ghi rõ điều này, `handler.go:144-146`).

### 2.4 Tool Executor — `internal/agent/tools.go`

- Interface `ToolExecutor` (`tools.go:14-20`): `Execute(ctx, name, params)` + `ListTools()`.
- `LocalToolExecutor` — implementation đầu tiên, chạy tool **trực tiếp trên host**, timeout `30s` mỗi tool (`tools.go:51`, `106`). Interface cho phép swap sang sandbox (Docker) sau.
- **4 built-in tools** (`tools.go:57-97`):

| Tool | Mô tả | Chạy bằng |
|---|---|---|
| `read_file` | Đọc file | `os.ReadFile` |
| `write_file` | Ghi file (create/overwrite) | `os.WriteFile` |
| `run_command` | Chạy lệnh shell | `exec.CommandContext(ctx, "sh", "-c", ...)` |
| `list_files` | Liệt kê thư mục | `os.ReadDir` |

Kết quả trả về JSON: `{"result": "..."}` hoặc `{"error": "..."}`. ⚠️ **Bảo mật**: `run_command` không sandbox, dùng được `workdir` tuỳ ý — đúng cho học tập, không phù hợp production.

### 2.5 Batch Scheduler — `internal/inference/batch_scheduler.go`

Lõi của "continuous batching" (hiện là **static batch**). Thay vì request nối đuôi nhau (1 GPU = 1 request/lúc), gom request đến gần nhau vào 1 forward pass.

```
Handler 1 ──┐
Handler 2 ──┼──► submitCh (cap 100) ──► collectorLoop
Handler 3 ──┘        │                        │
                     │  100ms window           │ gRPC BatchGenerate
                     │  hoặc max batch (4)     │
                 events channels ◄─────────────┘  route theo request_id
```

**`collectorLoop`** (`batch_scheduler.go:104-138`):

1. Block chờ request đầu tiên.
2. Trong `batchWindow` (**100ms**) hoặc đến `maxBatchSize` (**4**), gom thêm request (dừng khi đủ batch hoặc hết window).
3. Dispatch batch trong goroutine mới, quay lại block chờ batch kế tiếp.

**`dispatchBatch`** (`batch_scheduler.go:141-226`):

1. Build `BatchGenerateRequest` proto + index `request_id → batchItem`.
2. Mở gRPC `BatchGenerate` stream.
3. Vòng đọc response, route từng event về đúng channel theo `request_id` (token/tool_use/final).
4. Khi nhận `final` → đóng channel của request đó, xoá khỏi index.
5. Lỗi stream → gửi `STOP_ERROR` cho các request còn dang dở. `failAll` dùng khi không mở được stream.

Hằng số: `DefaultBatchWindow = 100ms`, `DefaultMaxBatchSize = 4` (`batch_scheduler.go:15-19`).

### 2.6 gRPC Client — `internal/inference/client.go`

- `Client` giữ cả 2 service stubs: `InferenceServiceClient` + `BatchInferenceServiceClient` (`client.go:20-21`).
- Config gRPC: `insecure` credentials (local), `MaxCallRecvMsgSize = 100MB`, `MaxCallSendMsgSize = 10MB` (`client.go:25-31`).
- `GenerateStream` (`client.go:112-237`): chuyển internal → proto, mở server-streaming `Generate`, đọc events trong goroutine, đóng channel khi hết. Có xử lý phân biệt: `io.EOF` = hết bình thường; nếu `ctx.Err() != nil` → `STOP_CANCELLED`; lỗi khác → `STOP_ERROR`.
- `BatchGenerate` (`client.go:105-107`): gọi thẳng RPC batch, trả về `pb.BatchInferenceService_BatchGenerateClient`.

> ⚠️ Hiện tại **chỉ đường batch được sử dụng** bởi agentic loop (`loop.go` gọi `scheduler.Submit`, không gọi `client.GenerateStream`). Đường single `Generate` tồn tại trong code nhưng là nhánh chết khi runtime — xem §9.1.

---

## 3. Thành phần Python Worker

Entry point: `python-worker/worker/server.py` (chạy `python -m worker.server`, có thể chạy trực tiếp `python -m worker`). gRPC `asyncio` server, mặc định port **50051**.

### 3.1 gRPC Server — `server.py`

- **`InferenceServicer.Generate`** (`server.py:39-108`):
  - Chuyển proto → dict (`_messages_from_proto`, `_tools_from_proto`).
  - Ghép system prompt thành message đầu nếu có.
  - Tạo `cancel_event` + task `watch_cancel` poll `context.cancelled()` mỗi **100ms** → set event khi Go cancel.
  - Stream events từ `engine.generate(...)` → `_build_response` → `context.write`.
  - Lỗi → gửi final `STOP_ERROR` rồi thoát.
- **`BatchInferenceServicer.BatchGenerate`** (`server.py:200-260`):
  - Lazy tạo `BatchEngine` (cần model + tokenizer từ engine đã load).
  - Chuyển toàn bộ batch → dict, gọi `batch_engine.generate_batch(...)`.
  - Stream từng `(request_id, event)` về → `_build_batch_response` → `context.write`.
  - Lỗi → gửi `STOP_ERROR` cho **từng** request trong batch.
- Server bootstrap (`server.py:316-362`): load engine, register 2 servicer, giới hạn message 100MB/10MB, keepalive 30s/10s, graceful shutdown trên SIGINT/SIGTERM (đợi `stop_event`, `server.stop(5)`, `engine.unload()`).

### 3.2 InferenceEngine (single) — `engine.py`

- `MODEL_ID = "Qwen/Qwen2.5-Coder-7B-Instruct"` (`engine.py:29`) — **ungated**, không cần HF login. Docs cũ ghi 3B (docstring trong `engine.py` vẫn ghi "Llama 3.2 3B" — xem §9.3).
- Config 4-bit (`engine.py:35-40`): `BitsAndBytesConfig(load_in_4bit=True, bf16 compute, double_quant, nf4)` — đủ khít 12GB VRAM của RTX 3060. `device_map="auto"`.
- `generate()` (`engine.py:148-277`):
  1. `_build_prompt` dùng `tokenizer.apply_chat_template(messages, tools=...)` (format OpenAI-style cho template, kèm tool calling nếu có).
  2. `model.generate(..., streamer=TextIteratorStreamer)` chạy trong **daemon thread** (để vòng lặp chính có thể kiểm tra cancel).
  3. Đọc streamer từng token → yield `{"type": "token"}`; giữa các token kiểm tra `cancel_event` → nếu set, yield `STOP_CANCELLED`.
  4. Hết → xác định `stop_reason`: `completion_tokens >= max_new` → `STOP_MAX_TOKENS`; ngược lại heuristic tool-call → `STOP_TOOL_USE`; không → `STOP_END_TURN`. Kèm `usage`.
- Phát hiện tool call là **heuristic** (`engine.py:279-292`): tìm marker trong text sinh ra — `<|python_tag|>`, `<function=`, `<tool_call>`, `{"tool_call`. Bình luận trong code ghi rõ sẽ thay bằng parse đúng khi tự viết tokenizer (Tuần 3-4).
- Singleton `get_engine()` (`engine.py:302-307`).

### 3.3 BatchEngine — `batch_engine.py`

`generate_batch(requests)` (`batch_engine.py:78-192`):

1. Build prompt từng request (system prompt được prepend thành message đầu).
2. `tokenizer(prompts, padding=True, truncation=True, max_length=8192)` — pad về dài nhất trong batch.
3. `model.generate(**inputs, max_new_tokens=max(các max_tokens), temperature=avg(...))` trong `torch.no_grad()`.
4. **Split output theo từng request** (dùng `attention_mask` để lấy prompt_len), decode từng token, `yield (req_id, {"type": "token"})`.
5. Cuối mỗi request: `STOP_MAX_TOKENS` nếu đạt giới hạn, ngược lại `STOP_END_TURN` + `usage`.

> ⚠️ **BatchEngine (engine transformers) không phát hiện tool call** — chỉ sinh `STOP_END_TURN` / `STOP_MAX_TOKENS`. Hệ quả: trên transformers, đường batch (đường duy nhất mà agentic loop dùng) **không bao giờ tạo ra `tool_use`** (xem §9.1). Trên engine **llama**, `LlamaBackend` xử lý tool call native (xem §3.4).

### 3.4 LlamaBackend — `engines/llama/`

Engine llama chạy model GGUF qua **llama-server** (llama.cpp) thay vì transformers: worker spawn subprocess và proxy gRPC → OpenAI-compatible HTTP. Entry `worker/server.py` gọi `get_backend(engine_name, model_id, gguf, llama_port, llama_bin)` (`server.py:315-316`) để chọn engine lúc khởi động.

- **`EngineBackend`** (`engines/base.py`): interface chung — `generate(...)`, `generate_batch(...)` → yield event dict (token / tool_use / final). `get_backend()` là registry chọn implementation.
- **`TransformersBackend`** (`engines/transformers.py`): wrap `InferenceEngine` + `BatchEngine` hiện có (Qwen2.5-Coder-7B) — giữ nguyên hành vi cũ.
- **`LlamaServer`** (`engines/llama/server.py`): spawn `llama-server` subprocess với flags `--host 127.0.0.1 --port 8081 --n-gpu-layers -1 --ctx-size 8192 --threads 8`; chờ `/health` (timeout), log ra `llama-server-8081.log`, stop khi worker tắt.
- **`LlamaClient`** (`engines/llama/client.py`): proxy request → `POST {base_url}/v1/chat/completions` (OpenAI-style, SSE stream); httpx transport.
- **`LlamaBackend`** (`engines/llama/backend.py`): map gRPC request ↔ OpenAI body (`"model": "qwen3.5-9b"`, `messages`, `tools`, `tool_choice:"auto"`), map response events (token delta, `tool_calls`, `finish_reason`) → event dict giống TransformersBackend. **Tool calling native** của llama-server → sinh `STOP_TOOL_USE` + `tool_calls` thật — nhánh tool-use của agentic loop hoạt động trên engine này (xem §9.1).

**GGUF / binary:** `models/Qwen3.5-9B-Q4_K_M.gguf` + `models/llama.cpp/llama-server.exe` (CUDA 12.4). Flags worker: `--engine llama --gguf <path> --llama-port 8081 --llama-bin <bin>`.

Proxy flow:
```
gRPC Generate / BatchGenerate
  → LlamaBackend (LlamaClient) → POST /v1/chat/completions (SSE stream)
  → llama-server (token / tool_calls)
  → LlamaBackend map → event dict (token / tool_use / final) → gRPC response
```

---

## 4. Proto Contract — `proto/inference.proto`

Hai service, cả hai đều **server-streaming** (Python gửi nhiều response cho 1 request):

```proto
service InferenceService {
  rpc Generate(GenerateRequest) returns (stream GenerateResponse);
}
service BatchInferenceService {
  rpc BatchGenerate(BatchGenerateRequest) returns (stream BatchGenerateResponse);
}
```

- `GenerateRequest` (`inference.proto:15-29`): `request_id`, `session_id`, `messages` (internal canonical), `system_prompt`, `sampling_params`, `tools` (JSON Schema string).
- `GenerateResponse` (`inference.proto:66-83`): đa dạng theo `event_type` (`EVENT_TOKEN`/`EVENT_TOOL_USE`/`EVENT_FINAL`) — mỗi event chỉ populate field tương ứng (`token`, `tool_use`, hoặc `stop_reason`+`finish_reason`+`usage`).
- `StopReason` enum (`inference.proto:99-106`): `STOP_END_TURN`, `STOP_MAX_TOKENS`, `STOP_TOOL_USE`, `STOP_CANCELLED`, `STOP_ERROR`.
- `SamplingParams` (`inference.proto:58-64`): `max_tokens` (1024), `temperature` (0.7), `top_p` (0.9), `top_k` (50), `stop_sequences`.
- `BatchGenerateResponse` (`inference.proto:137-151`): giống `GenerateResponse` + thêm `request_id` để Go route về đúng channel.

**Codegen:** Go dùng `protoc-gen-go-grpc` → `go-server/internal/inference/pb/`; Python dùng `grpcio-tools` → `python-worker/worker/pb/` (sinh lại bằng `python -m worker.generate_proto`).

---

## 5. Luồng dữ liệu chi tiết

### 5.1 Request OpenAI non-stream

Trước đây API server expose 2 dialect — Messages API + OpenAI `/v1/chat/completions` — normalize về cùng internal format. Ngày 2026-08-15 repo **collapse về chỉ còn OpenAI `/v1/chat/completions`** để có một contract duy nhất: bỏ adapter Messages API, hàm non-stream của dialect cũ, JSON response kiểu Messages API, và dropdown protocol trong UI. Luồng non-stream giờ chạy như sau:

```
POST /v1/chat/completions
  → decode OpenAIRequest → validate
  → session (x-session-id hoặc uuid mới)
  → OpenAIToInternal → []session.Message + systemPrompt
  → set systemPrompt vào session
  → handleOpenAINonStream
      → cho từng user message: loop.RunStreaming(...)
          → scheduler.Submit → collectorLoop → gRPC BatchGenerate (batch có thể 1)
          → Python BatchEngine: model.generate → stream (req_id, token)
          → Go route về đúng channel → loop phát LoopEvent...
      → gom content: text + tool_calls
  → JSON response {choices[0].message, finish_reason, usage}
```

### 5.2 Request streaming (SSE)

Giống 5.1 nhưng mỗi `LoopEventToken` được viết ngay vào SSE + `Flush()` (`handler.go:135-137`). Tốc độ token phụ thuộc trực tiếp vào TPOT của model (~75–80ms/token theo `docs/BENCHMARK.md`).

### 5.3 Agentic loop có tool call

```
user msg → model → model trả STOP_TOOL_USE + toolCalls
  → loop execute từng tool (LocalToolExecutor, timeout 30s)
  → lưu tool_result vào session
  → iteration kế: model nhìn lại toàn bộ history (kèm tool results) → tiếp tục
  → ... đến khi STOP_END_TURN hoặc đủ 10 iterations
```

Lưu ý: trên engine **transformers**, `tool_use` **không bao giờ** được sinh ra ở đường batch (§9.1) — nhánh này chưa kích hoạt ở runtime. Trên engine **llama**, nhánh này **chạy thật**: model gọi tool, Go executor execute, verified E2E (xem §9.1).

### 5.4 Concurrent requests (continuous batching)

```
Handler A, B, C đến gần nhau
  → cả 3 Submit vào submitCh
  → collectorLoop gom trong 100ms (hoặc tới batch=4)
  → 1 gRPC BatchGenerate với [A,B,C]
  → Python: 1 model.generate(batch=3) — GPU xử lý 3 sequence trong forward pass
  → stream (req_id, token) → Go route về 3 channel riêng → 3 SSE riêng biệt
```

Lợi ích định lượng từ `docs/BENCHMARK.md`: throughput single ~13 tok/s → batch 4 đạt **~54 tok/s (3.8×)** nhờ GPU tận dụng tốt hơn.

---

## 6. Cancel propagation

Chain cancel trải suốt từ client đến GPU (`CONTEXT.md` ghi "cancel từng request riêng không ảnh hưởng request khác trong batch"):

```
Client disconnect
  → r.Context().Done()
  → handler: goroutine gọi cancel() (handler.go:91-95)
  → ctx truyền vào loop.RunStreaming
  → loop: check ctx.Done() mỗi iteration; batch scheduler: select ctx.Done() khi route event
  → gRPC stream context bị cancel → Python watch_cancel (poll 100ms) set cancel_event
  → engine.generate: kiểm tra cancel_event giữa các token → yield STOP_CANCELLED
  → stream kết thúc, VRAM được giải phóng (model.generate thoát sớm)
```

Giới hạn hiện tại: cancel phía Python là **poll 100ms** (không phải event-driven), và trong batch mode model vẫn phải chạy hết forward pass của batch (chỉ bỏ qua việc gửi kết quả của request bị cancel).

---

## 7. Batching & mô hình concurrency

| Khía cạnh | Thiết kế hiện tại |
|---|---|
| Đơn vị dispatch | Batch tĩnh: gom trong 100ms hoặc đủ 4 request |
| GPU thực thi | `model.generate()` với batched inputs (padding + attention mask) |
| Routing | Go giữ `request_id → channel` map, route từng event |
| Backpressure | `submitCh` cap 100 — nếu đầy, `Submit` block (backpressure tự nhiên) |
| Dynamic batching | **Chưa** — không chèn/xoá sequence giữa các decode step (đánh dấu cho Tuần 7-8) |
| Multi-user | Session in-memory, phân biệt bằng `x-session-id`; không auth |
| Channel buffer | Loop events: 64; gRPC events: 100 |

**Điểm đáng lưu ý về batching hiện tại:** do `BatchEngine` chạy `model.generate()` đồng bộ cho cả batch và **chỉ stream kết quả sau khi batch hoàn tất**, các request trong batch thực chất xếp hàng ngay tại Python (sau 1 forward pass chung). Đây là static batching, không phải true continuous batching — đúng như nhận định trong `CONTEXT.md`.

---

## 8. Model & inference

- **Model (transformers, default):** Qwen2.5-Coder-7B-Instruct, quant 4-bit NF4 (bitsandbytes), `device_map="auto"` (RTX 3060 12GB). Docs cũ ghi "Qwen 2.5 3B" — code chạy 7B từ trước.
- **Model (llama):** Qwen3.5-9B, GGUF Q4_K_M (`models/Qwen3.5-9B-Q4_K_M.gguf`), chạy qua llama-server (llama.cpp, CUDA 12.4 build) thay vì bitsandbytes — GPU layers do llama-server quản lý (`--n-gpu-layers -1`), không dùng `device_map`. Chi tiết §3.4.
- **Tokenizer:** BPETokenizer tự viết (`worker/model/tokenizer/bpe.py` — byte-level BPE). Load `vocab.json`/`merges.txt`/`tokenizer_config.json` có sẵn của Qwen (IDs khớp 100%), tự implement byte-encoder, regex pre-tokenization, BPE merge, decode (kể cả streaming `StreamingDecoder`), batch pad/truncate (`build_inputs`). Đảm nhận encode/decode trong cả `engine.py` lẫn `batch_engine.py`. Có bộ test đối chiếu ID == HF (`python-worker/tests/test_tokenizer.py`, 40 test).
- **Chat template:** `hf_tokenizer.apply_chat_template` — chỉ dùng HF `AutoTokenizer` cho phần Jinja template (build prompt *string*), không dùng để token hoá (quyết định D1, spec `docs/superpowers/specs/2026-08-08-tokenizer-design.md`). Hỗ trợ tool calling qua tham số `tools`.
- **Streaming:** `TextIteratorStreamer` + daemon thread. Streamer chỉ cần `tokenizer.decode(ids, **kwargs)` — BPETokenizer duck-type vừa khớp (xác minh transformers 4.50.3).
- **Sinh token hiện tại do HuggingFace đảm nhiệm** (sampling loop + KV cache của HF). Theo roadmap, còn lại: Tuần 5-6 tự viết sampling (greedy/temperature/top-p/top-k), Tuần 7-8 tự quản lý KV cache + dynamic batching.
- **Token counting:** Go dùng heuristic `chars/4`; Python đếm chính xác qua tokenizer (`token_count` = `len(encode(text))`) khi trả `usage`.

Số liệu benchmark tham khảo (`docs/BENCHMARK.md`): TTFT ~70–85ms, TPOT ~75–82ms, single throughput ~13 tok/s.

---

## 9. Hạn chế & lỗ hổng tích hợp hiện tại

Phần này ghi lại những khác biệt giữa **kiến trúc lý tưởng** (trong comment/`CONTEXT.md`) và **hành vi thực tế** của code — quan trọng khi debug hoặc tiếp tục phát triển.

### 9.1 Tool-calling: hoạt động trên engine llama, chết trên engine transformers

> ✅ **Engine llama (Qwen3.5-9B): tool-use ĐÃ HOẠT ĐỘNG và verified E2E (2026-08-10).** llama-server hỗ trợ tool calling native (structured output) → `LlamaBackend` parse `tool_calls` từ response → sinh event `STOP_TOOL_USE` + `tool_calls` thật → nhánh tool-use của `loop.go` (step 7) kích hoạt ở runtime. E2E đã xác nhận: model gọi `read_file("test.txt")`, Go executor chạy tool, model trả lời với nội dung file. Không còn là nhánh chết trên engine này.
>
> ⚠️ **Engine transformers (Qwen2.5-Coder-7B): vẫn chết.** Các giới hạn dưới đây áp dụng cho đường transformers.

- Agentic loop chỉ gọi `scheduler.Submit` → đi qua `BatchGenerate` → `TransformersBackend` → `BatchEngine.generate_batch`.
- `BatchEngine` **đã stream token theo thời gian thực** (TTFT ~0.8s): `model.generate(streamer=BatchTokenStreamer)` chạy trong thread, streamer decode token mới của từng request (skip lần `put(prompt)` đầu, `unsqueeze(-1)` vì `_sample` squeeze thành [batch]) → push vào `queue.Queue` → async generator yield về client; request gặp EOS/max token được cắt sớm độc lập, batch vẫn chạy cho request khác (`batch_engine.py`).
- Nhưng `BatchEngine` vẫn **không bao giờ yield `tool_use`** — chỉ `token`/`final` với `STOP_END_TURN`/`STOP_MAX_TOKENS` (heuristic tool-call của `InferenceEngine` cũng không được dùng ở đường batch).
- Hệ quả: trên transformers, nhánh tool-calling của `loop.go` (step 7) không kích hoạt ở runtime. Model vẫn nhận `tools` trong prompt (qua chat template), nhưng tool call model tự sinh ra bị coi là text thường và stop reason là `STOP_END_TURN`.
- Cần sửa nếu muốn tool calling chạy trên transformers: hoặc cho `BatchEngine`/`TransformersBackend` parse tool call từ output batch, hoặc loop dùng đường `GenerateStream` (single) cho các request cần tool.

### 9.2 Tool definitions từ client chưa được nối

- `adapters.go` có `ToolsToInternal` / `OpenAIToolsToInternal` và handler parse `req.Tools` vào struct, nhưng **không truyền xuống loop**.
- Loop luôn dùng `l.tools.ListTools()` — tức **4 built-in tools** của `LocalToolExecutor`, không phải tool client khai báo trong request.

### 9.3 Inconsistency về model trong comment

- `server.py` help string cho flag `--model` vẫn ghi "Llama 3.2 3B" (`server.py:365`) và `engine.py` docstring nói "Llama 3.2 3B", nhưng `MODEL_ID` thực tế là **Qwen/Qwen2.5-Coder-7B-Instruct** (`engine.py:29`). Các comment heuristic tool-call cũng nói format Llama (`<|python_tag|>`) — với Qwen chat template, tool call có format khác, nên heuristic có thể không khớp.

### 9.4 Flag `--max-concurrent` không tác dụng khi ≤ 1

- `main.go:42-44`: `SetMaxBatchSize` chỉ gọi khi `maxConcurrent > 1`; mặc định flag = `1`. Batch size thực tế luôn là `DefaultMaxBatchSize = 4` cho tới khi override. Log ở `main.go:45-46` cũng in `max_batch = *maxConcurrent` (sai khi flag = 1).

### 9.5 Khác

- **No auth/rate-limit/persistence** — session in-memory, mất khi restart; `x-session-id` do client tự đặt.
- **`run_command` không sandbox** — chạy trực tiếp trên host với `sh -c`.
- **Không observability** — chỉ log stdout; chưa có metrics/tracing/cost metering (xem spec `docs/superpowers/specs/2026-08-06-llm-inference-scale-design.md`).
- Cancel Python là poll 100ms.

---

## 10. Ngăn xếp công nghệ

| Layer | Tech |
|---|---|
| Go server | Go 1.25.6, `net/http` + `http.ServeMux`, `google.golang.org/grpc`, `google/uuid` |
| Python worker | Python ≥3.11, `grpcio` (aio), `torch`, `transformers`, `bitsandbytes`, `accelerate` (+ `httpx` cho llama proxy) |
| Model | Transformers: Qwen/Qwen2.5-Coder-7B-Instruct (4-bit NF4) · Llama: Qwen3.5-9B GGUF Q4_K_M (llama-server) |
| Contract | Protobuf 3, server-streaming gRPC |
| Streaming | gRPC (Python→Go), SSE (Go→Client) |

## 11. Cách chạy

```bash
# Terminal 1: Python worker (mặc định port 50051) — engine transformers (default, Qwen2.5-Coder-7B)
cd python-worker && python -m worker.server

#   ... hoặc engine llama (Qwen3.5-9B GGUF): spawn llama-server trên port 8081.
#   (thêm --llama-bin ..\models\llama.cpp\llama-server.exe nếu llama-server chưa có trên PATH)
cd python-worker && .\.venv\Scripts\python -m worker.server --engine llama --gguf ..\models\Qwen3.5-9B-Q4_K_M.gguf

# Terminal 2: Go server (mặc định port 8080)
cd go-server && go run ./cmd/server/

# Test nhanh (OpenAI adapter) — content là STRING thường:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'

# Health
curl http://localhost:8080/health
```

## 12. Map với learning roadmap

| Giai đoạn | Nội dung | Trạng thái trong code |
|---|---|---|
| Tuần 1-2 | End-to-end: proto → gRPC → Go → model | ✅ Đã xong |
| Tuần 1-2 | OpenAI protocol + SSE + agentic loop | ✅ Đã xong (tool-calling còn lỗ hổng, §9.1) |
| Tuần 1-2 | Continuous batching (static) | ✅ Đã xong (static batch) |
| Tuần 3-4 | Tự viết tokenizer (BPE) | ✅ Đã xong — `worker/model/tokenizer/`, 40 test đối chiếu == HF (§8) |
| Bổ sung | Engine llama (Qwen3.5-9B GGUF, llama-server proxy) — ngoài roadmap gốc | ✅ Đã xong — tool calling hoạt động & verified E2E trên engine này (§3.4, §9.1) |
| Tuần 5-6 | Tự viết sampling | 🔜 Thay `model.generate` param |
| Tuần 7-8 | Tự quản lý KV cache + dynamic batching | 🔜 Thay phần lõi `BatchEngine` |
| Tuần 9+ | Forward pass, prefix caching, PagedAttention | 🔜 Tương lai |
