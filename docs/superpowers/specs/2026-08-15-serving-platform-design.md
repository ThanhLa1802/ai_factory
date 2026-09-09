# Design — Tiến hóa AI Factory thành Serving & Data Engineering Platform

- **Ngày**: 2026-08-15
- **Trạng thái**: Đã duyệt qua brainstorming (user duyệt toàn bộ design) — chờ review spec trước khi lập kế hoạch
- **Phạm vi**: Tiến hóa **AI Factory** (Go server + Python worker) thành một **AI Model Serving & Data Engineering Platform** theo reference `AI_Model_Serving_Data_Engineering_Platform_README.md`: thêm control plane, multi-tenant auth, inference gateway, Kafka event pipeline, usage analytics, quota & billing, observability. **Giữ nguyên** Go server + Python worker làm lõi; agentic loop/chat UI là phase sau.

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng AI Factory (đã xác minh)

- **Go server**: HTTP handlers, SSE streaming, session manager (in-memory, 8K ctx), agentic loop (max 10 iter), `LocalToolExecutor` (4 tools), `BatchScheduler` (static batch 100ms, batch ≤ 4), gRPC client.
- **Python worker**: 2 engine (`transformers` Qwen2.5-Coder-7B 4-bit NF4 + `llama` Qwen3.5-9B GGUF), tokenizer BPE tự viết khớp 100% HF, gRPC server-streaming (single + batch).
- **Chưa có**: auth, multi-tenant, persistence, usage tracking, observability.
- **Lỗ hổng liên quan** (`docs/ARCHITECTURE.md` §9): tool-calling chết trên engine transformers (§9.1), tool client chưa nối (§9.2), flag `--max-concurrent ≤ 1` (§9.4).

### 1.2 Reference spec

`AI_Model_Serving_Data_Engineering_Platform_README.md` — spec production cho platform gồm: control plane (tenant/model/template/deployment), inference gateway, Kafka event architecture, usage data engineering (raw → aggregation → analytics), observability, Superset dashboard. Non-goals rõ ràng: **không** GPU management, VM lifecycle, storage provisioning — tất cả là external.

### 1.3 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Tiến hóa AI Factory** — không phải dự án mới; giữ Go server + Python worker. |
| D2 | **Hướng production/portfolio** — PostgreSQL, Redis, Kafka, Prometheus, chạy single-machine dev. |
| D3 | **Control plane bằng Go** — một ngôn ngữ, một API surface, tái dùng session/adapters. |
| D4 | **Agentic loop + chat UI → phase sau** — giữ code, tập trung thuần serving trước. |
| D5 | **Hướng A: "Platform bao quanh core"** — thêm lớp mới bằng Go xung quanh core hiện có, không rewrite. |
| D6 | **User management 2 track** — human accounts (RBAC/JWT) cho control plane + API keys cho data plane. |
| D7 | **Thêm quota + billing** — quota là budget enforcement tại gateway; billing là package đọc aggregation + pricing (gateway không biết giá). |

---

## 2. Kiến trúc tổng thể

### 2.1 Sơ đồ

```
Tenant/User
    │
    ▼
API Gateway (Go — MỘT binary, hai mặt)
    │
    ├── Control Plane API     /api/v1/* (tenants, models, templates, deployments, usage, billing)
    │        │
    │        ▼
    │   Control Plane Service ────► PostgreSQL (system of record)
    │        │
    │        ▼
    │   Event Bus (Kafka) ──────────► serving.deployment.events / serving.audit.events
    │
    └── Inference Gateway     /v1/chat/completions (OpenAI-compatible — đã có)
             │
             ├─ Auth: API key → tenant
             ├─ Rate limit (Redis) + Quota check (Redis counter)
             ├─ Routing: model → deployment (READY) → worker endpoint
             ▼
        Model Runtime (Python Worker) — sau ServingRuntimeAdapter (gRPC)
             │
             ▼
        serving.inference.events → Usage Worker → Raw → Hourly/Daily aggregation → Analytics views
                                                                    │
                                          ┌───────────────┬────────┴───────────┐
                                          ▼               ▼                   ▼
                                     Quota reconcile  Billing service      Superset
```

### 2.2 Nguyên tắc chính

- **Một Go binary** xử lý control plane lẫn inference gateway; các worker (usage, deployment) là **goroutine background** trong cùng process — sau này tách process được vì mỗi cái là package độc lập.
- **PostgreSQL** = system of record cho control state + analytics views (single DB cho dev, không cần warehouse riêng).
- **Kafka** = event backbone; consumer **idempotent** (dedup theo `event_id`).
- **Redis** = rate limit + quota counters.
- **Prometheus** = metrics (`/metrics`); **log/slog** JSON = structured logs.
- Deployment ops **bất đồng bộ** (API → event → worker → `ComputeProvider`), trả `202 Accepted`.
- Tất cả timestamp **UTC** (reference Rule 7).

---

## 3. Control plane domain model

### 3.1 Bảng PostgreSQL (migrations bằng goose)

| Bảng | Cột chính |
|---|---|
| `tenants` | id, name, status, created_at, updated_at |
| `users` | id, username/email, password_hash (argon2/bcrypt), status, created_at |
| `tenant_memberships` | user_id, tenant_id, role |
| `api_keys` | id, tenant_id, name, key_hash, status, expires_at, last_used_at, scope |
| `tenant_quotas` | tenant_id, quota_type, limit_value, period |
| `models` | id, name, description, task, framework, status |
| `model_versions` | id, model_id, version, artifact_uri, metadata_json, status |
| `serving_templates` | id, name, description, runtime, status |
| `serving_template_versions` | id, template_id, version, image, command, environment, config_schema |
| `deployments` | id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status |
| `deployment_revisions` | id, deployment_id, revision, spec_json, created_at, created_by |
| `endpoints` | id, deployment_id (1:1), path, protocol, status |
| `pricing` | model_id, region, price_per_million_input_tokens, price_per_million_output_tokens, price_per_gpu_hour, currency |
| `billing_usage` | tenant_id, period, request_count, input_tokens, output_tokens, gpu_hours, amount |
| `invoices` | tenant_id, period, line_items_json, total, status |

**Analytics (cùng DB):** `raw_inference_events`, `usage_hourly`, `usage_daily`, `tenant_usage_daily`, `model_usage_daily`, `region_usage_daily` (chi tiết §7).

### 3.2 Deployment state machine

```
PENDING → PROVISIONING → STARTING → READY ⇄ DEGRADED
   │          │             │          │
   └──── FAILED ←──── lỗi từ bất kỳ trạng thái nào ────┘

READY → STOPPING → STOPPED
```

- **Explicit state machine**, mỗi transition phát event (`deployment_ready`, `deployment_failed`, ...).
- Mọi thay đổi config tạo **deployment revision** (rollback/audit/reproducibility — reference §25).

### 3.3 ServingRuntimeAdapter

```go
type ServingRuntimeAdapter interface {
    Create(ctx context.Context, spec DeploymentSpec) error
    Start(ctx context.Context, d *Deployment) error
    Stop(ctx context.Context, d *Deployment) error
    Restart(ctx context.Context, d *Deployment) error
    Delete(ctx context.Context, d *Deployment) error
    GetStatus(ctx context.Context, d *Deployment) (Status, error)
    HealthCheck(ctx context.Context, d *Deployment) error
}
```

- **Adapter đầu tiên**: Python worker (gọi qua gRPC như hiện tại) — tận dụng pattern `EngineBackend` đã có.
- Adapter tương lai: vLLM, Triton (chỉ đổi endpoint).

### 3.4 External ComputeProvider (boundary — không implement GPU/VM)

```go
type ComputeProvider interface {
    RequestCapacity(ctx, spec) (workloadRef, error)
    ReleaseCapacity(ctx, d) error
    GetWorkloadStatus(ctx, d) error
    UpdateWorkload(ctx, d, spec) error
}
```

- `MockComputeProvider` cho dev — chạy toàn bộ control plane flow không cần GPU thật.

---

## 4. User management

### 4.1 Human accounts (control plane)

- `users` + `tenant_memberships` (user ↔ tenant ↔ role). Một user có thể thuộc nhiều tenant; `PLATFORM_ADMIN` có thể không gắn tenant.
- Roles: `PLATFORM_ADMIN`, `TENANT_ADMIN`, `TENANT_DEVELOPER`, `TENANT_VIEWER`.
- Login: `POST /api/v1/auth/login` (username + password) → **JWT** (access + refresh). Mọi request control plane mang token; middleware giải token lấy `user_id` + `tenant_id` **từ auth context**, check role.
- **Không bao giờ tin tenant_id từ client body** (reference Rule 3 — chống cross-tenant).

### 4.2 API keys (data plane)

- `Authorization: Bearer sk-xxx` → hash → lookup `api_keys` → resolve tenant.
- Chỉ lưu **key_hash**; secret trả đúng **1 lần** lúc tạo (`POST /api/v1/api-keys`).
- Scope tùy chọn: giới hạn theo deployment/model.

### 4.3 Bootstrap dev

- Seed: 1 `PLATFORM_ADMIN` + 1 tenant mặc định + 1 API key test.
- Tenant/user/key tạo qua control plane API (admin tạo; chưa cần open signup).

---

## 5. Inference gateway

### 5.1 Luồng request

```
Client ──► POST /v1/chat/completions (Bearer sk-xxx)
            │
            ├─ 1. Auth: hash API key → tenant_id
            ├─ 2. Rate limit (Redis): tenant:rpm, tenant:tpm, deployment:concurrency
            ├─ 3. Quota check (Redis counter): tokens/requests per period → QUOTA_EXCEEDED
            ├─ 4. Routing: model → deployment (status=READY, đúng tenant) → worker endpoint
            │      (routing cache in-memory, refresh từ PostgreSQL khi deployment đổi)
            ├─ 5. Forward → Model Runtime (gRPC, như hiện tại) + timeout/retry
            └─ 6. Emission: usage event + metrics
```

### 5.2 Thay đổi giao thức gRPC

- Thêm **usage info** vào message cuối của stream: `input_tokens`, `output_tokens`, `ttft_ms` (worker dùng tokenizer tự viết đếm chính xác).
- Gateway tính `latency_ms`, `queue_time_ms`, `total_tokens` từ timestamps + usage info → đủ dữ liệu usage event.
- `proto/inference.proto` sinh lại 2 phía (Go + Python).

### 5.3 Rate limit + quota

- **Rate limit** = throughput (rpm/tpm/concurrency) — Redis fixed-window/token bucket.
- **Quota** = budget dài hạn (tokens/tháng, requests/tháng, GPU-hours/tháng, concurrent) — chi tiết §8.
- Thứ tự: rate limit → quota → route.

---

## 6. Events (Kafka)

### 6.1 Topics

| Topic | Event types |
|---|---|
| `serving.deployment.events` | deployment_created / requested / starting / ready / degraded / failed / stopping / stopped |
| `serving.inference.events` | inference_started / completed / failed / timeout |
| `serving.audit.events` | admin actions (user, key, template CRUD) |

### 6.2 Envelope chuẩn

```json
{
  "event_id": "evt_001",
  "event_type": "inference_completed",
  "event_version": 1,
  "timestamp": "2026-08-15T02:00:00Z",
  "tenant_id": "tenant_001",
  "resource_id": "deploy_123",
  "trace_id": "trace_123",
  "payload": {}
}
```

### 6.3 Idempotency

- Consumer dedup theo `event_id` (PK/unique) — reference Rule 5.
- DLQ: `serving.inference.events.dlq`, `serving.deployment.events.dlq`.
- Retry exponential backoff (1s→2s→4s→8s→16s).

---

## 7. Usage pipeline

```
Inference Gateway ──► Kafka ──► Usage Worker (consumer goroutine)
                                        │
                                        ▼
                               raw_inference_events (append-only, immutable, event_id PK)
                                        │
                                        ▼   (incremental aggregation — ticker, watermark theo event_id)
                               usage_hourly / usage_daily
                               tenant_usage_daily / model_usage_daily / region_usage_daily
                                        │
                                        ▼
                               Analytics views (vw_*) → Superset
```

### 7.1 Các lớp

- **Raw**: append-only, replayable, dedup theo `event_id`.
- **Aggregation**: incremental (không scan toàn bộ raw mỗi lần dashboard hỏi — reference §40); upsert theo watermark.
- **Views**: `vw_active_tenants`, `vw_gpu_usage_by_region`, `vw_gpu_usage_by_tenant`, `vw_top_tenants`, `vw_model_usage`, `vw_template_usage`, `vw_deployment_usage`, `vw_request_latency`, `vw_request_error_rate`.

### 7.2 Metrics mỗi bảng

`request_count`, `success_count`, `error_count`, `input_tokens`, `output_tokens`, `total_tokens`, `avg/p50/p95/p99_latency_ms`, `avg_ttft_ms`.

### 7.3 GPU hours

`gpu_seconds = replica_count × gpu_count_per_replica × active_seconds`; `gpu_hours = gpu_seconds / 3600`. Nếu external infra cung cấp số liệu GPU authoritative → ưu tiên nguồn đó (reference §16).

---

## 8. Quota

### 8.1 Loại quota

| Loại | Ví dụ | Enforcement |
|---|---|---|
| Token | 50M tokens/tháng | Gateway pre-flight + reconcile |
| Request | 1M requests/tháng | Gateway pre-flight + reconcile |
| GPU-hours | 200 GPU-hours/tháng | Từ aggregation (hậu kỳ) |
| Concurrent | 8 request đồng thời | Gateway pre-flight |

### 8.2 Enforcement + reconciliation

- **Pre-flight** (gateway): đọc Redis counter `tenant:{id}:{period}:tokens` so với limit → vượt thì `429 QUOTA_EXCEEDED` (kèm `x-quota-remaining` trong response).
- **Reconciliation**: usage aggregator sau mỗi event cộng dồn vào quota counter → gần real-time.
- Phân biệt rõ với rate limit: quota = budget dài hạn, rate limit = throughput ngắn hạn; **cả hai cùng tồn tại**.

---

## 9. Billing

### 9.1 Nguyên tắc

- **Gateway không biết giá** (reference §42: "Do not hard-code pricing into the inference gateway"). Gateway chỉ sinh **usage quantity**.
- Billing = package riêng đọc `usage_daily` (aggregation đã có) + bảng `pricing` → ghi `billing_usage` / `invoices`.

### 9.2 Flow

```
usage_daily (aggregation) ──► Billing service ──► pricing (model/region)
                                    │
                                    ▼
                          billing_usage (quantity × price)
                          invoices (draft, line items + total)
                                    │
                                    ▼
                  API: /api/v1/billing/usage, /api/v1/billing/invoices/{id}
```

### 9.3 Ngoài scope (YAGNI)

Cổng thanh toán, PDF invoice, thuế/VAT, proration phức tạp, multi-currency đầy đủ — chỉ tính phí quantity × giá.

---

## 10. Observability

### 10.1 Metrics (Prometheus, `/metrics`)

```
serving_requests_total                        {tenant, deployment, model, region, status}
serving_request_errors_total
serving_request_duration_seconds
serving_time_to_first_token_seconds
serving_input_tokens_total / serving_output_tokens_total
serving_active_requests / serving_queue_depth
serving_deployment_replicas / serving_deployment_ready_replicas
serving_deployment_status
```

Tránh label high-cardinality (`request_id`, prompt, full error) — reference §21.

### 10.2 Logs

- `log/slog` JSON: `{timestamp, level, service, event, tenant_id, deployment_id, trace_id}`.
- **Không log**: API key, auth header, raw prompt/completion (reference §36, Rule 6).

### 10.3 Tracing

- M1–M4: dùng `trace_id` (UUID) truyền qua header + event để nối log ↔ event.
- **OpenTelemetry tracing end-to-end → M5** (sau khi platform ổn định; span: gateway → routing → worker).

---

## 11. Repo structure

```
go-server/
├── cmd/server/main.go              # khởi động: HTTP server + worker goroutines
└── internal/
    ├── api/                        # HTTP handlers: control plane + inference gateway (mở rộng)
    ├── auth/                       # JWT, API key, RBAC, middleware
    ├── controlplane/               # tenant/model/template/deployment domain + state machine
    ├── runtime/                    # ServingRuntimeAdapter interface + worker adapter
    ├── events/                     # Kafka producer/consumer, envelope, topics
    ├── usage/                      # usage worker, raw + aggregation + views
    ├── billing/                    # pricing, billing_usage, invoices
    ├── observability/              # metrics registry, slog setup
    ├── session/ + agent/           # giữ nguyên (phase sau: agentic/chat)
    └── inference/                  # gRPC client, batch scheduler (giữ)
```
```
deployments/docker-compose.yml      # postgres, redis, kafka, prometheus, superset (optional)
migrations/                         # goose SQL migrations
```

Cấu hình qua env vars + flags. `python-worker` và `proto/` không đổi cấu trúc (chỉ proto thêm usage info).

---

## 12. Lộ trình MVP

| Milestone | Nội dung | Demo được |
|---|---|---|
| **M1** Foundation + Control plane | PostgreSQL + migrations + docker-compose; auth (users/tenants/API keys/JWT/RBAC); CRUD models/templates/deployments + state machine + revisions; quota schema; /health, /metrics, slog | Tạo tenant → tạo model → tạo deployment; state machine chuyển trạng thái |
| **M2** Runtime adapter + async deploy | `ServingRuntimeAdapter`; adapter Python worker (gRPC); deployment worker (Kafka consumer); `MockComputeProvider` | POST /deployments → 202 → PENDING→READY qua worker |
| **M3** Inference gateway + usage events + quota | API key auth trên `/v1/chat/completions`; rate limit (Redis); quota enforcement; routing model→deployment→worker; gRPC usage info; emit inference events → raw table | Gọi inference bằng API key; usage event ghi vào raw; vượt quota → 429 |
| **M4** Usage analytics + billing | Aggregation hourly/daily; tenant/model/region tables + gpu_hours; views vw_*; `/api/v1/usage`; billing service + `/api/v1/billing/*`; Prometheus dashboard | Superset hiện: top tenants, GPU by region, model usage; invoice draft |
| **M5** Phase sau | OTel tracing, bật lại agentic loop + chat UI, autoscaling/canary | — |

> **Phạm vi planning:** kế hoạch triển khai chia theo milestone. Plan đầu tiên tập trung **M1** (Foundation + Control plane); các milestone sau có plan riêng sau khi M1 hoàn tất.

---

## 13. Testing strategy

- **Unit**: state machine, auth/RBAC, quota tính toán, event serialization, idempotency, routing, rate limiting.
- **Integration**: PostgreSQL (goose migrate + repo), Redis, Kafka (produce/consume), usage consumer → raw → aggregation.
- **Contract**: event schema, `ServingRuntimeAdapter`/`ComputeProvider` interface, OpenAI-compatible API.
- **E2E**: create tenant → create model → create template → create deployment → READY → send inference bằng API key → receive response → consume event → aggregate → query usage → query billing.

---

## 14. Non-goals / Scope boundaries

- **Không** GPU management, VM lifecycle, storage provisioning (reference §3) — chỉ interface `ComputeProvider`.
- **Không** agentic loop/chat UI trong M1–M4 (D4).
- **Không** OTel tracing trong M1–M4 (chỉ `trace_id`).
- **Không** open signup, OAuth/SSO, 2FA.
- **Không** thanh toán thật, PDF invoice, thuế.
- **Không** autoscaling/canary/blue-green trong MVP.

---

## 15. Lỗ hổng hiện tại liên quan (giữ mở)

- Tool-calling chết trên engine transformers (§9.1) — sẽ được giải quyết khi bật lại agentic phase sau.
- Tool từ client chưa nối (§9.2), flag `--max-concurrent ≤ 1` (§9.4) — fix nhỏ, làm khi đụng tới.
