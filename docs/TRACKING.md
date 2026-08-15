# AI Factory — Theo dõi tiến độ

File này track **dự án đang ở phần nào** trong learning roadmap: checklist chi tiết từng giai đoạn, link tới code, và các việc đang treo.

- Cập nhật gần nhất: **2026-08-15** (xem [Nhật ký cập nhật](#nhật-ký-cập-nhật))
- Map code ↔ roadmap chi tiết: `docs/ARCHITECTURE.md` §12
- Tổng quan ngắn: `CLAUDE.md` → mục Learning Roadmap

---

## 📍 Hiện tại đang ở đâu

> **Đang ở giai đoạn Tuần 5–6 — Tự viết sampling loop.**

| Đã xong | Đang làm | Chưa làm |
|---|---|---|
| Tuần 1–2: E2E pipeline, OpenAI protocol, SSE, agentic loop, static batching | Tuần 5–6: sampling loop (hiện do HF `model.generate()` đảm nhiệm) | Tuần 7–8: KV cache + dynamic batching |
| Tuần 3–4: Tokenizer byte-level BPE tự viết | | Tuần 9+: Forward pass, prefix caching, PagedAttention |
| Bonus: Engine llama (Qwen3.5-9B GGUF) + tool-calling E2E | | |

**Việc kế tiếp cụ thể:** thay các tham số của `model.generate()` (greedy / temperature / top-p / top-k) bằng một sampling loop tự viết, kèm test và benchmark đối chiếu.

---

## Tổng quan roadmap

| Giai đoạn | Nội dung | Trạng thái |
|---|---|---|
| Tuần 1–2 | E2E: proto → gRPC → Go → model; OpenAI protocol + SSE; agentic loop; static batching | ✅ Xong |
| Tuần 3–4 | Tự viết tokenizer byte-level BPE | ✅ Xong |
| Bổ sung | Engine llama (Qwen3.5-9B GGUF, llama-server proxy) | ✅ Xong |
| M2 — Runtime adapter + async deploy | ServingRuntimeAdapter + MockComputeProvider + Kafka events + deployment worker | ✅ Done |
| Tuần 5–6 | Tự viết sampling loop (greedy / temperature / top-p / top-k) | 🔜 Kế tiếp |
| Tuần 7–8 | Tự quản lý KV cache + dynamic batching | 🔜 Chưa |
| Tuần 9+ | Forward pass tự viết, prefix caching, PagedAttention | 🔜 Chưa |

---

## ✅ Giai đoạn 1 — Tuần 1–2: E2E + OpenAI protocol + agentic loop

Trạng thái: **✅ Xong**

- [x] Proto contract 2 service server-streaming — [`proto/inference.proto`](../proto/inference.proto)
- [x] gRPC server Python (InferenceServicer + BatchInferenceServicer) — [`python-worker/worker/server.py`](../python-worker/worker/server.py)
- [x] gRPC client Go + route event theo `request_id` — [`go-server/internal/inference/client.go`](../go-server/internal/inference/client.go)
- [x] BatchScheduler static batching (gom 100ms, batch ≤ 4) — [`go-server/internal/inference/batch_scheduler.go`](../go-server/internal/inference/batch_scheduler.go)
- [x] OpenAI protocol (`/v1/chat/completions` → canonical format) — [`go-server/internal/api/adapters.go`](../go-server/internal/api/adapters.go)
- [x] SSE streaming — [`go-server/internal/api/sse.go`](../go-server/internal/api/sse.go)
- [x] Session manager in-memory (8K ctx, truncation) — [`go-server/internal/session/`](../go-server/internal/session/)
- [x] Agentic loop (max 10 iter) — [`go-server/internal/agent/loop.go`](../go-server/internal/agent/loop.go)
- [x] ToolExecutor + 4 built-in tools — [`go-server/internal/agent/tools.go`](../go-server/internal/agent/tools.go)
- [x] Cancel propagation client → Go → gRPC → Python

## ✅ Giai đoạn 2 — Tuần 3–4: Tokenizer byte-level BPE tự viết

Trạng thái: **✅ Xong** (spec đã duyệt, tích hợp vào pipeline)

- [x] Byte-level BPE — [`python-worker/worker/model/tokenizer/bpe.py`](../python-worker/worker/model/tokenizer/bpe.py)
- [x] Byte-encoder — [`python-worker/worker/model/tokenizer/byte_level.py`](../python-worker/worker/model/tokenizer/byte_level.py)
- [x] IDs khớp 100% với HF (test đối chiếu) — [`python-worker/tests/test_tokenizer.py`](../python-worker/tests/test_tokenizer.py)
- [x] Dùng cho encode/decode/batch trong cả `engine.py` lẫn `batch_engine.py`
- [x] HF `AutoTokenizer` chỉ giữ cho `apply_chat_template` (quyết định D1)

## ✅ Bổ sung — Engine llama (Qwen3.5-9B GGUF)

Trạng thái: **✅ Xong** — tool-calling **hoạt động & verified E2E** trên engine này

- [x] EngineBackend interface + registry (chọn bằng `--engine`) — [`python-worker/worker/engines/base.py`](../python-worker/worker/engines/base.py)
- [x] LlamaServer — subprocess manager + health check — [`python-worker/worker/engines/llama/server.py`](../python-worker/worker/engines/llama/server.py)
- [x] LlamaClient — httpx SSE transport — [`python-worker/worker/engines/llama/client.py`](../python-worker/worker/engines/llama/client.py)
- [x] LlamaBackend — OpenAI mapping, tool_calls, batch — [`python-worker/worker/engines/llama/backend.py`](../python-worker/worker/engines/llama/backend.py)
- [x] Download Qwen3.5-9B GGUF + cài llama-server — [`scripts/`](../scripts/)
- [x] Tests — `test_llama_backend.py`, `test_llama_client.py`, `test_llama_server.py`, `test_engines.py`, `test_server_backend.py`

## 🔜 Giai đoạn 3 — Tuần 5–6: Tự viết sampling loop

Trạng thái: **Đang làm / kế tiếp** — hiện do HF `model.generate()` đảm nhiệm

- [ ] Sampling greedy (argmax)
- [ ] Temperature scaling
- [ ] Top-p (nucleus) sampling
- [ ] Top-k sampling
- [ ] Thay thế tham số `model.generate()` bằng sampling loop tự viết
- [ ] Tích hợp vào `InferenceEngine` / `BatchEngine` (giữ streaming + batch)
- [ ] Test đối chiếu kết quả + benchmark hiệu năng

## 🔜 Giai đoạn 4 — Tuần 7–8: Tự quản lý KV cache + dynamic batching

Trạng thái: **Chưa bắt đầu**

- [ ] Tự quản lý KV cache (thay thế phần lõi `BatchEngine`)
- [ ] Dynamic/continuous batching thay cho static batch hiện tại
- [ ] Tối ưu bộ nhớ + benchmark

## 🔜 Giai đoạn 5 — Tuần 9+: Forward pass tự viết

Trạng thái: **Chưa bắt đầu**

- [ ] Forward pass tự viết
- [ ] Prefix caching
- [ ] PagedAttention

---

## ⚠️ Việc treo / lỗ hổng đang mở

Chi tiết: `docs/ARCHITECTURE.md` §9.

- [ ] **Tool-calling chết trên engine transformers** (§9.1) — đường batch không phát hiện `tool_use` (chỉ sinh `STOP_END_TURN`/`STOP_MAX_TOKENS`). Hiện chỉ hoạt động trên engine llama.
- [ ] **Tool từ client chưa nối** (§9.2) — loop luôn dùng 4 built-in tools, tool client khai báo trong request bị bỏ qua.
- [ ] **Bug nhỏ `--max-concurrent`** (§9.4) — flag ≤ 1 không ghi đè batch size; log `max_batch` sai khi flag = 1.
- [x] **Auth trên inference** (consumer slice): JWT + API key bắt buộc trên `/v1/chat/completions`; UI login/chat/keys.
- [ ] Chưa có: persistence, sandbox cho `run_command`, observability (usage/tracing/cost).

---

## Nhật ký cập nhật

| Ngày | Thay đổi |
|---|---|
| 2026-08-15 | A5 (Reliability) — bắt đầu: retry/backoff/jitter (`internal/retry` + áp dụng vào worker provisioning: RequestCapacity, adapter.Start). Còn lại A5: idempotency, circuit breaker, backpressure, load shedding. |
| 2026-08-15 | M3 — inference routing (model→deployment READY, tenant isolation) + rate limit (Redis: tenant RPM + concurrency). Spec docs/superpowers/specs/2026-08-15-inference-routing-rate-limit-design.md. |
| 2026-08-15 | M2 — Runtime adapter + async deploy (ServingRuntimeAdapter + MockComputeProvider + Kafka events + deployment worker) ✅ Done — spec docs/superpowers/specs/2026-08-15-serving-platform-design.md. |
| 2026-08-15 | Consumer slice: auth trên inference (JWT/API key) + UI 3 trang; hoãn M2 Task 2–6; spec `docs/superpowers/specs/2026-08-15-consumer-auth-ui-design.md`. |
| 2026-08-15 | Tạo file tracking; xác nhận các giai đoạn 1–2 + engine llama đã xong; giai đoạn 3 (sampling) là kế tiếp. |
