# Design Doc — Backend cho LLM Inference quy mô khủng

- **Ngày**: 2026-08-06
- **Trạng thái**: Draft (chờ review)
- **Phạm vi**: Brainstorm kiến thức BE + lộ trình scale-up dự án `ai_factory` từ single-node lên quy mô hàng chục triệu requests / hàng chục tỷ tokens mỗi ngày.
- **Mục tiêu**: Trả lời hai câu hỏi — *(1)* BE cần kiến thức gì ở scale này, *(2)* làm như thế nào — và map ngược về dự án `ai_factory` để có con đường execute thực tế.

---

## 1. Quy mô mục tiêu — đọc con số thành ràng buộc kỹ thuật

| Metric | Con số thô | Trung bình/giây | Peak (ước 3–5x) |
|---|---|---|---|
| Requests | 50M/ngày | ~580 RPS | 1,7K – 3K RPS |
| Output tokens | 30B/ngày | ~350K tok/s | 700K – 1M tok/s |
| Output/request | 30B ÷ 50M | **~600 tokens/req** | — |

### Hệ quả 1 — Workload là *generation-heavy*, không phải *request-heavy*

600 output tokens/request nghĩa là đa số workload là chat dài / coding / agentic. Khó khăn nằm ở **throughput sinh token** (GPU-bound), không phải số lượng HTTP request. RPS ~580 là con số bình thường với một web server thường; 350K tok/s là bài toán **GPU fleet**.

### Hệ quả 2 — Số luồng song song cần thiết

Nếu mỗi sequence decode ~30–40 tok/s (memory-bandwidth bound), để đạt 350K tok/s cần **~10K sequence đang chạy đồng thời**. Đây là 10K thao tác *forward pass xen kẽ trên GPU*, không phải 10K connection — không giải quyết bằng thread pool thông thường.

### Định cỡ fleet (order-of-magnitude)

- RTX 3060 12GB + Qwen 3B 4-bit (thiết bị hiện tại của dự án): ~50 tok/s/stream, ~300–600 tok/s aggregate khi batch đầy.
- Để đạt 350K tok/s: ~**600–1.200 GPU** cấp RTX 3060.
- Kết luận: ở scale này, bài toán không còn là "viết inference engine" — mà là **orchestration, queueing, và cost**.

### Latency budget (provider-grade)

- **TTFT** (time-to-first-token): < 1–2s
- **TPOT** (time-per-output-token): < 30ms
- Vi phạm hai con số này là lỗi kiến trúc, không phải lỗi model.

---

## 2. Kiến trúc tham chiếu

```
                          ┌────────────────────────────────────────────┐
 Client (SSE/gRPC) ─────► │ Gateway: auth, rate-limit, quota, protocol │
                          └──────────────────┬─────────────────────────┘
                                             │
                          ┌──────────────────▼─────────────────────────┐
                          │ Router/Scheduler: bounded queue, priority, │
                          │ prefix-cache lookup, route→replica có cache │
                          └──────────────────┬─────────────────────────┘
                                             │
                    ┌────────────────────────▼─────────────────────────┐
                    │      Inference cluster (nhiều replica)           │
                    │  vLLM/SGLang/TensorRT-LLM (TP/PP)  +  Autoscaler │
                    └────────────┬────────────────────┬────────────────┘
                                 │                    │
                    ┌────────────▼──────┐   ┌─────────▼──────────────┐
                    │ KV/Prefix cache   │   │ Agentic loop executor  │
                    │ (distributed)     │   │ (sandbox, timeouts)    │
                    └────────────┬──────┘   └─────────┬──────────────┘
                                 └────────┬───────────┘
                                          ▼
                    Observability: metrics, traces, logs, cost-metering
```

**Điểm quan trọng nhất**: tầng *giữa gateway và GPU*. Ở scale này bottleneck không phải model — là **queue**, **cache**, **scheduler** và **giá tiền**.

---

## 3. Tám trụ kiến thức BE

### 3.1 Serving & inference engine

- **Continuous batching** (iteration-level scheduling): static batching chết ở scale này vì request có độ dài khác nhau; phải chèn/xoá sequence vào batch mỗi iteration.
- **KV cache & PagedAttention**: bộ nhớ GPU cho cache chiếm 50–70%; phải quản lý được. **Prefix caching** (cache phần prompt trùng nhau) → cắt TTFT và giảm chi phí prefill.
- **Prefill vs decode**: prefill compute-bound, decode memory-bandwidth-bound. **Chunked prefill** để request dài không chặn decode cả batch.
- **Speculative decoding / Medusa**: tăng throughput 1.5–3x bằng draft model.
- **Parallelism**: Tensor Parallelism (chia model ngang nhiều GPU, cần NVLink/NCCL), Pipeline Parallelism, Data Parallelism, Expert Parallelism (MoE).
- **Quantization** (FP8/AWQ/GPTQ) + nén KV cache: lever cost lớn nhất.
- **Structured output / constrained decoding**: JSON mode, function calling — quan trọng cho tool-use và agentic loop.

### 3.2 Distributed systems & concurrency

- **Go**: goroutine/chan/worker pool, `errgroup`, `sync.Pool`, GC tuning, pprof. *(Dự án đã có nền — lợi thế.)*
- **Queueing theory**: Little's law `L = λW`. Bounded queue + backpressure là bắt buộc — GPU là tài nguyên không buffer được.
- **Load shedding**: quá tải phải ưu tiên, không tăng timeout. Chia tier (interactive vs batch, paid vs free), preemption.
- **Rate limiting** phân tán (token bucket trên Redis), idempotency + retry với jitter/backoff, **circuit breaker**, **bulkhead**.

### 3.3 Networking & streaming

- **SSE/HTTP streaming qua proxy**: nginx/ALB buffer mặc định → chết TTFT. Phải tắt buffering, dùng chunked transfer.
- **HTTP/2 vs gRPC**: dự án đã dùng gRPC streaming Go↔Python — đúng hướng. Ở scale cần multiplexing, keep-alive, connection pool.
- **WebSocket** cho tool-loop: stream event ngược client (kiểu Anthropic `tool_use` events).
- **Latency budget**: TTFT < 1–2s, TPOT < 30ms.

### 3.4 Storage & caching

- **Redis/Valkey**: session, rate limit, distributed lock, index của prefix-cache.
- **Kafka/Pulsar**: hàng đợi bền vững cho offline logs, replay request, pipeline eval.
- **Object storage** (S3/minio): logs, prompts, evals, fine-tune data.
- **Trạng thái phân tán**: session manager phải rời bộ nhớ một máy → key theo user + affinity, hoặc stateless + state store bên ngoài.

### 3.5 Observability

- **Golden signals**: traffic, latency (tách TTFT/TPOT/total), errors, **saturation** — theo dõi *GPU util + queue depth + KV cache hit rate*, không phải CPU.
- **OpenTelemetry**: trace xuyên client→gateway→engine (mỗi request một trace id).
- **Cost attribution**: mỗi request gán được chi phí token → metering theo tenant/endpoint/model.

### 3.6 SRE & reliability

- Capacity planning: mô hình *tokens/sec per GPU* → định cỡ fleet → autoscale dựa trên queue depth, không phải CPU.
- Multi-region failover; canary/blue-green khi deploy model weight mới (deploy "model" khác hẳn deploy "code").
- Chaos testing; load testing (k6, hoặc replay production logs).

### 3.7 Security & safety

- AuthN/AuthZ (API key, JWT, RBAC), tenant isolation.
- **Content moderation + prompt injection defense** (scale này là mục tiêu tấn công).
- Compliance: PII redaction trong logs, data residency.
- Anti-abuse: rate limit theo key, cost cap.

### 3.8 Cost engineering

- **Metering & billing theo token**.
- Right-sizing fleet; spot instance; chống GPU idle.
- **Prefix cache + quantization = 2 lever tiết kiệm lớn nhất**.

---

## 4. Quyết định then chốt: buy vs build

| Phần | Scale nhỏ (ai_factory) | Scale khủng |
|---|---|---|
| Forward pass | Python tự viết (học) | **vLLM / SGLang / TensorRT-LLM** (hoặc API provider) |
| Batching | Không có / thủ công | Continuous batching của engine |
| Queue + scheduling | In-memory | Router riêng + distributed queue |
| Session | In-memory map | Redis + affinity |
| Tool executor | Local | Sandbox hoá, scale ngang, timeouts |
| Observability | Log | Prometheus + OTel + dashboard |

**Nguyên tắc**: kiến thức BE khác biệt không nằm ở việc viết lại transformer loop — mà ở mọi thứ xung quanh nó. Phần duy nhất nên tự viết là **layer application** (gateway, router, agentic loop, metering). Phần còn lại mua/lease.

---

## 5. Map về `ai_factory` — hiện trạng, lỗ hổng, thay đổi

### Hiện trạng (đã đúng)

Skeleton hiện tại đã là kiến trúc chuẩn ở dạng mini: **Go (SSE + session + agentic loop + tool executor) → gRPC → Python worker**.

- ✅ **Protocol adapters** (Anthropic + OpenAI) → đúng mô hình "translation layer". Ở scale thêm: schema versioning, feature flags per endpoint.
- ✅ **Session manager in-memory** → sẽ là lỗ hổng đầu tiên cần phân tán.
- ✅ **Tool executor** → đã có interface `ToolExecutor` để swap sandbox.
- ✅ **gRPC server-streaming** (Python→Go) + **SSE** (Go→Client) → đúng pattern streaming.

### Lỗ hổng

- ❌ Bounded queue + backpressure.
- ❌ Prefix-cache.
- ❌ Autoscaling.
- ❌ Observability (metrics/tracing/cost).
- ❌ Auth/rate-limit.
- ❌ Cost metering.

### Thay đổi quan trọng nhất

Python worker hiện tại load model + forward pass thủ công — đúng cho học, nhưng **đừng scale theo hướng đó**. Bước chuyển quyết định: **thay Python worker bằng vLLM/SGLang** (cùng gRPC / OpenAI-compatible API), giữ nguyên Go layer. Vừa học được nội dung (batching, KV cache) vừa có con đường lên production thật.

---

## 6. Lộ trình giai đoạn (roadmap)

1. **Giữ nguyên** — hiểu TTFT vs TPOT, vì sao decode chậm hơn prefill, vì sao batching quan trọng.
2. **Thêm vào `ai_factory`** — bounded queue + backpressure; tách session ra Redis; streaming correctness (client disconnect, heartbeat).
3. **Swap engine** — Go nói chuyện với vLLM/SGLang (local, 1 GPU); học KV cache / prefix caching / continuous batching bằng cách đọc số liệu.
4. **Horizontal scale** — nhiều replica + router + autoscale theo queue depth.
5. **Observability + cost + SRE** — metrics, tracing, metering, load test.
6. *(Tùy chọn)* **Advanced** — speculative decoding, MoE, multi-region.

### Ưu tiên đi sâu (cho người đang ở `ai_factory`)

1. **Queue + backpressure + scheduling** — bài toán trung tâm của scale.
2. **vLLM/SGLang swap** — triển khai luôn vào `ai_factory`.
3. **Streaming qua proxy + TTFT/TPOT** chi tiết.
4. **Buy-vs-build decision framework** với con số định cỡ cụ thể.

---

## 7. Tiêu chí thành công (định nghĩa "đạt scale")

- TTFT < 1–2s, TPOT < 30ms ở peak.
- Queue depth bounded, không có unbounded memory growth.
- KV cache hit rate được đo và tối ưu (prefix caching).
- Cost attribution: mọi request truy vết được chi phí.
- Degrade gracefully: quá tải → 429/prioritize, không phải 500/timeout rồi client retry.

## 8. Mở / câu hỏi để ngỏ

- Phạm vi: tự host GPU fleet vs gọi provider API (Anthropic/OpenAI/DeepSeek) — ảnh hưởng mạnh tới roadmap.
- Workload mix thực tế (chat? coding? agentic?) — ảnh hưởng tới tỷ lệ prefill/decode, cần prefix cache hay không.
- Yêu cầu compliance / data residency — ảnh hưởng tới multi-region.

## 9. Nguồn tham khảo

- vLLM docs — continuous batching, PagedAttention, prefix caching.
- SGLang docs — RadixAttention, structured output.
- "LLM Inference" literature: prefill/decode, speculative decoding, TP/PP/EP.
- OTel + Prometheus + k6 — observability & load testing.
