# AI Factory — Learning Roadmap (bản riêng của dự án)

> **Nguồn tham khảo:** `c:\Users\thanh\Downloads\AI_Serving_Backend_Engineer_Learning_Roadmap.md` — roadmap generic cho Backend Engineer làm AI Model Serving ở scale production (hàng chục tỷ token/ngày, H100, vLLM/SGLang, Kubernetes, ClickHouse, Prometheus/OTel).
>
> File này là bản **điều chỉnh roadmap gốc vào stack thực tế của AI Factory**: thay tech generic bằng công nghệ dự án đang dùng, ghép với lộ trình **self-written inference** hiện có (`CLAUDE.md` → Learning Roadmap, `docs/TRACKING.md` → theo dõi tiến độ).
>
> - Track tiến độ từng giai đoạn: `docs/TRACKING.md`
> - Map code ↔ roadmap chi tiết: `docs/ARCHITECTURE.md` §12
> - Cập nhật lần cuối: **2026-08-15**

---

## 1. Đánh giá mức độ phù hợp của roadmap gốc

**Kết luận chung: roadmap gốc phù hợp về tư duy và cấu trúc, nhưng KHÔNG áp thẳng được** — nó nhắm tới hạ tầng production đa GPU/K8s/data-platform, trong khi AI Factory là dự án học tập single-box (RTX 3060 12GB, Go server + Python worker sidecar) có hướng đi riêng: **tự viết inference engine** thay vì dùng vLLM có sẵn.

### 1.1 Phần phù hợp — đã/đang áp dụng trong dự án

| Kỹ năng roadmap gốc | Tương ứng trong AI Factory | Trạng thái |
|---|---|---|
| API Gateway / OpenAI-compatible `/v1/chat/completions` | Go server, adapters.go, SSE | ✅ Xong (Tuần 1–2) |
| Streaming (SSE, client disconnect, cancel) | SSE + cancel propagation client→Go→gRPC→Python | ✅ Xong |
| Redis: rate limiting, concurrency counter | `internal/ratelimit` (tenant RPM + deployment concurrency) | ✅ Xong (M3) |
| Kafka: event bus, consumer, idempotency | Deployment events (`serving.deployment.events`), worker PENDING→READY | ✅ Xong (M2, Kafka optional / in-memory fallback) |
| PostgreSQL: control plane, transaction | Postgres lưu models/deployments | ✅ Xong (M2) |
| Auth / tenant / quota | Consumer slice: JWT + API key, UI login/chat/keys | ✅ Xong |
| Distributed Systems: retry, backoff, timeout, backpressure | `internal/retry` (exp backoff + jitter), `internal/circuitbreaker` (3-state CLOSED/OPEN/HALF-OPEN), BatchScheduler `TrySubmit` backpressure + load shedding → 503, cancel propagation | ✅ Xong |
| LLM Serving concepts (tokenization, prefill, decode, KV cache, batching, TTFT/TPOT) | Đúng phần lõi dự án — đang tự viết từng phần | 🔜 Đang làm |
| Observability (logs/metrics/traces, structured logging, không log prompt/key) | slog JSON toàn bộ, Prometheus `serving_*` (requests/duration/tokens/inflight/overloaded), trace span kiểu W3C `traceparent` (log-based, không OTel SDK), usage metering | ✅ Xong |
| System Design (10 câu hỏi §30) | Áp dụng khi review kiến trúc (routing, cancel, async deploy) | 🔜 Thường trực |

### 1.2 Phần KHÔNG khớp với dự án hiện tại (chỉ để tham khảo)

| Phần roadmap gốc | Vì sao không khớp |
|---|---|
| FastAPI / Python async | API layer của dự án là **Go** (HTTP/SSE/gRPC), không phải FastAPI |
| vLLM / SGLang, tensor/pipeline parallel, H100 | Dự án **tự viết inference** (transformers 4-bit + llama.cpp GGUF), single GPU 12GB — vLLM là "đích để hiểu", không phải stack để chạy |
| Kubernetes, GPU scheduling, affinity/taint | Dự án chạy localhost + `docker-compose` cho infra (postgres/kafka/redis) — K8s ngoài phạm vi giai đoạn này |
| ClickHouse, Superset, data modeling, billing | Chưa có scale data platform; usage metering (requests/tokens) là hướng mở sau |
| CUDA kernel / NCCL source | Chỉ cần "boundary knowledge" (đúng như roadmap gốc §31) |

### 1.3 Điểm dự án có mà roadmap gốc không nhấn

- **Self-written inference stack** (tokenizer → sampling → KV cache → forward pass): roadmap gốc dùng vLLM như black-box; AI Factory chọn tự viết để học sâu, đây là lộ trình gốc của dự án.
- **gRPC server-streaming giữa gateway và worker** (thay vì HTTP→vLLM): proto `inference.proto`, 2 service single + batch.
- **BatchScheduler static batching** (gom 100ms, batch ≤ 4) — sẽ tiến tới dynamic/continuous batching (đúng khái niệm roadmap gốc §13).

---

## 2. Lộ trình ghép cho AI Factory — 2 track song song

Dự án đang đi **2 track cùng lúc**. Track A theo roadmap gốc (backend/platform), Track B theo lộ trình tự viết inference của dự án.

### Track A — Backend / Platform (theo roadmap gốc, đã điều chỉnh stack)

| Giai đoạn | Nội dung | Trạng thái |
|---|---|---|
| A1. Gateway + protocol | Go HTTP/SSE, OpenAI adapter, gRPC client, agentic loop, tools | ✅ Xong |
| A2. Control plane | Postgres, deployment PENDING→READY, Kafka events, ServingRuntimeAdapter | ✅ Xong (M2) |
| A3. Routing + rate limit | Model→deployment READY (tenant-scoped), Redis RPM + concurrency | ✅ Xong (M3) |
| A4. Auth + tenant | JWT/API key trên inference, UI login/chat/keys | ✅ Xong |
| A5. Reliability | Retry/backoff/jitter (`internal/retry` + worker provisioning), idempotency (Kafka consumer state-machine guard + deploy `Idempotency-Key` + `idempotency_keys` table), circuit breaker (`internal/circuitbreaker` + worker), backpressure/load shedding (BatchScheduler `TrySubmit` → 503) | ✅ Xong |
| A6. Observability | Structured logs (slog JSON toàn bộ), metrics (Prometheus `serving_*`: requests, duration, tokens, inflight, overloaded), trace span kiểu W3C `traceparent` (dependency-free; OTel SDK có thể thay sau), usage metering (prompt/completion tokens) | ✅ Xong |
| A7. Data platform | Kafka → ClickHouse → Superset (nếu mở rộng) | ⛔ Hoãn |
| A8. Infra | Dockerfile hoàn chỉnh, K8s manifest (nếu cần) | ⛔ Hoãn |

### Track B — Self-written inference (lộ trình hiện có của dự án)

| Giai đoạn | Nội dung | Trạng thái |
|---|---|---|
| B1. E2E pipeline | proto → gRPC → Go → model, batching tĩnh | ✅ Xong |
| B2. Tokenizer | Byte-level BPE tự viết (khớp 100% HF) | ✅ Xong |
| B3. Sampling loop | Greedy / temperature / top-p / top-k (thay `model.generate()`) | 🔜 Kế tiếp (Tuần 5–6) |
| B4. KV cache + dynamic batching | Tự quản lý KV cache, continuous batching | 🔜 Tuần 7–8 |
| B5. Forward pass + prefix cache | Forward pass tự viết, prefix caching, PagedAttention | 🔜 Tuần 9+ |

---

## 3. Kế hoạch 6 tháng (điều chỉnh từ roadmap gốc §36)

| Tháng | Track A (Backend/Platform) | Track B (Self-written inference) |
|---|---|---|
| 1 | A5 Reliability: retry/backoff/idempotency + A6 Observability (structured log, metric, trace) | B3 Sampling loop: greedy → temperature → top-p → top-k + test đối chiếu |
| 2 | A6 tiếp: usage metering (requests/tokens/latency), không log prompt/key | B3 hoàn tất, tích hợp vào InferenceEngine/BatchEngine giữ streaming + batch |
| 3 | A7 nhập môn data: xuất event usage ra Kafka, consumer idempotent | B4 KV cache: thay phần lõi BatchEngine |
| 4 | System design: review 10 câu hỏi (§30) áp vào kiến trúc dự án | B4 dynamic/continuous batching + benchmark |
| 5 | A7/A8 tùy chọn: ClickHouse nhập môn hoặc Dockerfile production | B5 forward pass tự viết (bắt đầu) |
| 6 | Tổng kết: failure-handling drill (Redis/Kafka/worker chết, GPU OOM, cancel giữa stream) | B5 prefix caching / PagedAttention |

> Nguyên tắc từ roadmap gốc §40 — **học theo vấn đề, không học theo tutorial**:
> `Problem → Architecture → Technology → Implementation → Load test → Failure → Debug → Optimization`
> Đã được dự án áp dụng sẵn (mỗi giai đoạn là một vấn đề thật: batching, tokenizer, async deploy, rate limit).

---

## 4. Definition of "nắm thật chắc" (kế thừa từ roadmap gốc §37)

Nắm chắc một công nghệ khi tự trả lời được: (1) giải thích architecture, (2) giải thích trade-off, (3) viết implementation cơ bản, (4) debug lỗi, (5) benchmark, (6) scale, (7) monitor, (8) thiết kế failure handling, (9) biết khi nào KHÔNG nên dùng.

Ví dụ áp vào dự án:

- **Redis concurrency key**: vì sao cần TTL lease khi holder chết? Khi nào lease gây vượt limit? (trade-off đã ghi trong code `concurrencyLease`).
- **Kafka deployment events**: consumer chết 30 phút thì sao? Message xử lý 2 lần thì sao? (idempotency).
- **Batching**: static vs continuous batching khác nhau thế nào, vì sao continuous đạt throughput cao?

---

## 5. 10 kỹ năng cốt lõi (điều chỉnh cho AI Factory)

```text
1.  Go (HTTP/SSE/gRPC + concurrency)
2.  Protocol design (Protobuf, server-streaming)
3.  PostgreSQL (control plane)
4.  Redis (rate limit, concurrency, TTL/lease)
5.  Kafka (event bus, consumer, idempotency)
6.  Distributed Systems (retry, backoff, backpressure, cancel)
7.  LLM serving concepts (tokenization, prefill/decode, KV cache, batching)
8.  Self-written inference (sampling, forward pass)
9.  Observability (logs/metrics/traces)
10. System Design (multi-tenant, routing, failure handling)
```
