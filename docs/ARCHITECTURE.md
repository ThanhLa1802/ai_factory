# AI Factory — Kiến trúc chi tiết

> Tài liệu này mô tả kiến trúc **thực tế** của dự án dựa trên mã nguồn hiện tại, bổ sung cho `CONTEXT.md` (glossary ngắn) và `CLAUDE.md` (tổng quan + lộ trình). Nó đi sâu vào từng thành phần, luồng dữ liệu và các giới hạn tích hợp.

**Trạng thái doc:** cập nhật 2026-09-12 — khớp code sau M1–M3 (control plane + runtime adapter + routing/rate limit), A5 (reliability), A6 (observability), chat history + usage, và NextJS UI (`web/`). Nếu có thay đổi kiến trúc, cập nhật lại.

> **Đang tái kiến trúc:** Go server sẽ chuyển sang layout production (modular monolith + DI + composition root + multi-binary; Gin + GORM + gormigrate + viper + zap). Thiết kế: [`docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`](superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md) · Plan Phase 1: [`docs/superpowers/plans/2026-09-11-phase1-composition-root-di.md`](superpowers/plans/2026-09-11-phase1-composition-root-di.md). Tài liệu dưới đây mô tả **cấu trúc hiện tại** (trước tái kiến trúc).

---

## 1. Tổng quan

AI Factory là dự án học tập mô phỏng backend của Claude Code / ChatGPT, gồm **ba tầng + một control plane**:

| Tầng | Ngôn ngữ | Vai trò |
|---|---|---|
| **API Server + Control Plane** | Go | HTTP/SSE, OpenAI Chat Completions, session, agentic loop, tool executor, và control plane (tenant/model/template/deployment/API key/quota/usage) |
| **Inference Worker** | Python | Load model (HuggingFace / llama.cpp), tokenize, forward pass, sampling, batch inference |
| **Contract** | Protobuf | gRPC server-streaming giữa Go và Python |
| **Web UI** | NextJS | UI quản trị + chat (`web/`) |

Mô hình triển khai là **monorepo phẳng, Go = main server, Python = sidecar worker**, giao tiếp qua gRPC trên localhost. Go server vừa là **inference gateway** vừa là **control plane**; PostgreSQL là system of record, Redis cho rate limit, Kafka (tùy chọn) cho event deployment. Kiến trúc này tương đồng với production (API gateway + control plane + vLLM backend) ở dạng thu nhỏ.

### 1.1 Sơ đồ tổng thể

```
                    Client (SSE/HTTP) / NextJS UI (web/)
                              │
                              ▼
        ┌──────────────────────────────────────────────────────────────┐
        │                      GO SERVER (localhost:8080)               │
        │                                                              │
        │  Middleware: CORS → trace → logging → metrics                 │
        │                                                              │
        │  ┌── Inference gateway ─────────────────────────────────────┐ │
        │  │ /v1/chat/completions  (auth JWT/API key → routing →      │ │
        │  │   rate limit → agent.Loop → BatchScheduler)              │ │
        │  └──────────────────────────────┬───────────────────────────┘ │
        │                                 │ gRPC (localhost:50051)       │
        │  ┌── Control plane /api/v1/* ───┴───────────────────────────┐ │
        │  │ auth · tenants · models · templates · deployments ·      │ │
        │  │ api-keys · quotas · usage · sessions                      │ │
        │  └──┬───────────────┬──────────────────┬────────────────────┘ │
        │     │               │                  │                      │
        │     ▼               ▼                  ▼                      │
        │  PostgreSQL      Redis            Kafka (optional)            │
        │  (GORM+pgx)   (rate limit)   serving.deployment.events       │
        │     ▲                                │                       │
        │     └──── runtime.Worker ◄───────────┘ (async deploy)         │
        └──────────────────────────────────────┼───────────────────────┘
                                               ▼
        ┌──────────────────────────────────────────────────────────────┐
        │                      PYTHON WORKER                            │
        │  InferenceServicer + BatchInferenceServicer                    │
        │  └── EngineBackend (chọn bằng --engine)                       │
        │      ├── TransformersBackend  → Qwen2.5-Coder-7B (4-bit NF4)  │
        │      └── LlamaBackend         → Qwen3.5-9B GGUF (llama-server)│
        └──────────────────────────────────────────────────────────────┘
```

### 1.2 Hình dạng request trong hệ thống

Một request inference trải qua **3 lần "đổi format"**:

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
| `--max-concurrent` | `1` | Batch size (chỉ override khi `>1`) |
| `--ui-dir` | auto (`ui/` hoặc `../ui`) | Thư mục UI tĩnh |

**Cấu hình (viper: `configs/config.yaml`, env `AI_FACTORY_*` override — `internal/config/config.go`):**

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `AI_FACTORY_DATABASE_URL` | compose dev DSN | Postgres (bắt buộc; server fail boot nếu không kết nối) |
| `AI_FACTORY_JWT_SECRET` | `dev-secret-change-me` | Ký/verify JWT (≥16 ký tự) |
| `AI_FACTORY_LOG_LEVEL` | `info` | debug/info/warn/error |
| `AI_FACTORY_KAFKA_ADDR` | `localhost:9092` | Kafka broker (tùy chọn) |
| `AI_FACTORY_REDIS_ADDR` | `localhost:6379` | Redis cho rate limit |
| `AI_FACTORY_RATE_LIMIT_RPM` | `60` | RPM mỗi tenant |
| `AI_FACTORY_RATE_LIMIT_CONCURRENCY` | `4` | Concurrency mỗi deployment |
| `AI_FACTORY_SKIP_SEED` | — | `1` để bỏ qua seeding |
| `AI_FACTORY_ADMIN_USER/PASSWORD` | `admin` / `admin1234` | Tài khoản admin seed |
| `AI_FACTORY_DEMO_TENANT` | `acme` | Tenant demo seed |

**Thứ tự khởi động** (`cmd/server/main.go` → `internal/app`):

```
config.Load(path) → SetupLogger(zap JSON bridge slog)
  → di.NewContainer + app.RegisterAll (đăng ký provider)
  → app.NewAppFromContainer (force-resolve, fail-fast)
  → app.Run: database.Open(GORM) + Migrate(gormigrate) → controlplane (repos) → auth
    → seedAdmin → seedDemo → Kafka event bus (nếu down: MemoryEventBus + warn, worker tắt)
    → BatchScheduler → ToolExecutor → agent.Loop → session.Manager (GORM store)
    → api.Handler → gin.Engine (routes + /metrics) → middleware chain → ListenAndServe
    → SIGINT/SIGTERM → server.Close() → container.Shutdown/Close (reverse order)
```

**Dependency wiring** tập trung ở composition root `internal/app` (`registry.go` đăng ký provider, `app.go` sở hữu lifecycle); dependency được build lazy bởi `pkg/di` (Phase 1 ✅).

### 2.1 HTTP / API layer — `internal/api/` (Gin v1.11)

Handler nhận `*gin.Context`; route + auth middleware `gin.HandlerFunc`; global chain recovery → CORS → trace → logging → metrics (`internal/app`). SSE dùng `gin.ResponseWriter` (vẫn `http.Flusher`).

**Inference + session + UI** (`handler.go`, `Handler.RegisterRoutes`):

| Route | Method | Auth | Chức năng |
|---|---|---|---|
| `/v1/chat/completions` | POST | `auth.InferenceAuth` (JWT hoặc API key) | OpenAI Chat Completions (stream/non-stream) |
| `/health` | GET | — | Health JSON |
| `/api/v1/sessions` | GET | `RequireAuth` | Liệt kê session của tenant/user |
| `/api/v1/sessions/{id}` | GET/DELETE | `RequireAuth` | Đọc / xoá session |
| `/api/v1/sessions/{id}` | PATCH | `RequireAuth` | Đổi tiêu đề session |
| `/` , `/chat` , `/keys` | GET | — | UI tĩnh (HTML trong `ui/`) |
| `/concepts` | GET | — | Tài liệu technical concepts (HTML) |

**Control plane** (`controlplane.go:30-59`, `ControlPlaneHandler.RegisterRoutes`):

| Nhóm | Routes | Quyền |
|---|---|---|
| Auth | `POST /api/v1/auth/login` | — |
| API keys | `GET/POST /api/v1/api-keys`, `DELETE /api/v1/api-keys/{id}` | `key.manage` |
| Tenants | `GET/POST /api/v1/tenants` | `tenant.read` / `tenant.manage` |
| Models | `GET/POST /api/v1/models`, `GET /api/v1/models/{id}`, `POST /api/v1/models/{id}/versions` | `model.read` / `model.write` |
| Templates | `GET/POST /api/v1/templates`, `GET /api/v1/templates/{id}`, `POST /api/v1/templates/{id}/versions` | `template.read` / `template.write` |
| Deployments | `GET/POST /api/v1/deployments`, `GET /api/v1/deployments/{id}[/revisions]`, `POST /api/v1/deployments/{id}/{start\|stop}` | `deployment.read` / `deployment.write` |
| Quotas | `POST /api/v1/quotas`, `GET /api/v1/quotas` | `quota.manage` / `usage.read` |
| Usage | `GET /api/v1/usage?days=N` | `usage.read` |

**Xử lý request chung** (`handler.go`, OpenAI `/v1/chat/completions`):

1. Decode + validate body (`ValidateOpenAIRequest`).
2. Resolve tenant từ auth context (`auth.TenantIDFromContext` — **không tin body**).
3. `resolveForTenant`: `ResolveDeployment(tenant, model)` → READY deployment; áp RPM + concurrency limit (fail-open khi Redis lỗi); gắn serving labels cho metrics.
4. Lấy session ID từ header `x-session-id` (rỗng → UUID mới); `GetOrCreate(sessionID, tenantID, userID)`; session cross-tenant trả 404 để không lộ tồn tại.
5. `OpenAIToInternal` → internal messages + system prompt; system prompt lưu vào session, không nằm trong history.
6. `context.WithCancel(r.Context())` + goroutine chờ `r.Context().Done()` → khâu đầu của **cancel propagation** (§6).
7. Nhánh `Stream=true` → SSE; ngược lại gom events thành JSON response.

**Adapters** (`adapters.go`):

- `OpenAIToInternal`: system message → system prompt; `tool_calls`/`tool_call_id` map trực tiếp.
- `ToolsToInternal` / `OpenAIToolsToInternal`: chuyển tool definitions client gửi lên. ⚠️ **Hiện không được handler gọi** — xem §10.2.

**SSE writer** (`sse.go`): headers `text/event-stream`, `no-cache`, `keep-alive`, `X-Accel-Buffering: no`. Gửi token ngay mỗi lần + `Flush()`. Định dạng OpenAI stream: `chat.completion.chunk` với `delta.content` / `delta.reasoning_content` / `delta.tool_calls`, rồi chunk `finish_reason`, rồi `[DONE]`. Overloaded (backpressure) gửi frame lỗi `OVERLOADED`.

### 2.2 Auth — `internal/auth/`

- **Human accounts:** login `POST /api/v1/auth/login` trả **JWT** (HS256, TTL 8h) chứa user/tenant/role. Middleware `RequireAuth` verify token; `RequirePermission(action)` check RBAC.
- **API keys (data plane):** `InferenceAuth` chấp nhận JWT **hoặc** `Authorization: Bearer sk-...` → hash → lookup `api_keys` → resolve tenant. Secret chỉ trả 1 lần lúc tạo.
- **RBAC** (`rbac.go`): 4 role — `PLATFORM_ADMIN`, `TENANT_ADMIN`, `TENANT_DEVELOPER`, `TENANT_VIEWER` — map tới 11 action (`tenant.*`, `model.*`, `template.*`, `deployment.*`, `key.manage`, `quota.manage`, `usage.read`).
- Password hash: `internal/auth/password.go` (argon2/bcrypt). API key: `apikey.go` (generate + hash). JWT: `jwt.go`.

### 2.3 Control plane — `internal/controlplane/`

Service duy nhất (`Service`) gọi data access qua repository interface (impl GORM), gộp nhiều aggregate:

| Nhóm | File | Nội dung |
|---|---|---|
| Users/tenants | `users.go` | CreateUser/GetUserByUsername/CreateTenant/ListTenants/membership |
| Catalog | `catalog.go` | Model, ModelVersion, ServingTemplate, TemplateVersion |
| Deployment | `deployment.go`, `state.go` | CreateDeployment/TransitionDeployment/revisions/endpoints, state machine |
| Routing | `deployment.go` | `ResolveDeployment(tenantID, modelName)` → newest READY deployment |
| Quota | `quota.go` | UpsertQuota/ListQuotas |
| Usage | `usage.go` | RecordUsage + UsageSummary/UsageDaily/UsageByModel |
| Idempotency | `idempotency.go` | Resolve/Save `Idempotency-Key` |
| API keys | `types.go` (models) | Create/List/Delete API key |

**Deployment state machine** (`state.go`):

```
PENDING → PROVISIONING → STARTING → READY ⇄ DEGRADED
   │          │             │          │
   └──── FAILED ←─── lỗi từ bất kỳ trạng thái nào (terminal)
READY/DEGRADED → STOPPING → STOPPED → (restart) PENDING
```

- Mọi chuyển trạng thái đi qua `validTransitions` — chuyển không hợp lệ bị từ chối.
- Mọi thay đổi config tạo **deployment revision** (rollback/audit).

### 2.4 Session Manager — `internal/session/`

- Session **bền trong Postgres** (`sessions`, `messages` qua `store.go`/`PGStore`), không còn chỉ in-memory. `Manager` cache in-memory + ghi DB.
- `GetOrCreate(sessionID, tenantID, userID)`, `ListSessions`, `GetPersisted`, `RenameSession`, `DeleteSession`; tự đặt tiêu đề từ tin nhắn user đầu tiên (cắt 40 rune).
- `Session` lưu message history, `MaxTokens` (mặc định **8192**), `SystemPrompt`, title, model, timestamps.
- `EstimatedTokens()`: heuristic **chars/4**; con số chính xác do Python cung cấp qua `Usage`.

**Context truncation** — `TruncateMessages`:

- Trigger trong `loop.go` khi `EstimatedTokens() > 90% * MaxTokens` (`DangerZoneBeforeTruncate = 0.90`).
- Quét từ cuối về đầu, giữ message gần nhất vừa token budget.
- **Bảo toàn cặp tool**: nếu message giữ đầu tiên là `tool_result`, lùi thêm để giữ `tool_use` tương ứng; bỏ `tool_use` orphaned ở cuối.

### 2.5 Agentic Loop — `internal/agent/loop.go`

`Loop` giữ `BatchScheduler` + `ToolExecutor`. API: `RunStreaming(ctx, sess, userMessage, params) <-chan LoopEvent`.

`LoopEvent` có **6 loại** (`loop.go`): `Token`, `ToolUse`, `ToolResult`, `Final`, `Error`, `Reasoning` (reasoning token — display-only, không vào session context).

**Vòng lặp chính**, tối đa `MaxToolIterations = 10`:

```
1. Check ctx.Done() → nếu cancel, emit Final(STOP_CANCELLED)
2. sess.GetMessages() → truncate nếu vượt 90% budget
3. Lấy tool definitions từ executor.ListTools()
4. Build GenerateRequest → scheduler.Submit(ctx, req)
5. Tiêu thụ events từ gRPC:
     token      → forward ngay (LoopEventToken)
     reasoning  → forward display-only (LoopEventReasoning)
     tool_use   → lưu + emit (LoopEventToolUse)
     final      → đọc stop_reason; STOP_ERROR → emit LoopEventError, return
6. Lưu assistant message vào session (kèm tool_calls nếu có)
7. Nếu stop_reason == STOP_TOOL_USE và có toolCalls:
     → với mỗi tool: executor.Execute(ctx, name, args)
     → lưu tool message (Role=tool, ToolResult, IsError) vào session
     → emit LoopEventToolResult
     → continue (model nhìn thấy tool results)
8. Ngược lại: emit LoopEventFinal(stop_reason, usage), return
```

Tool results **không** stream về client — đưa vào session và dùng cho lượt inference tiếp theo.

### 2.6 Tool Executor — `internal/agent/tools.go`

- Interface `ToolExecutor`: `Execute(ctx, name, params)` + `ListTools()`.
- `LocalToolExecutor` chạy tool **trực tiếp trên host**, timeout `30s` mỗi tool. Interface cho phép swap sang sandbox (Docker) sau.
- **4 built-in tools**:

| Tool | Mô tả | Chạy bằng |
|---|---|---|
| `read_file` | Đọc file | `os.ReadFile` |
| `write_file` | Ghi file (create/overwrite) | `os.WriteFile` |
| `run_command` | Chạy lệnh shell | `exec.CommandContext(ctx, "sh", "-c", ...)` |
| `list_files` | Liệt kê thư mục | `os.ReadDir` |

Kết quả JSON: `{"result": "..."}` hoặc `{"error": "..."}`. ⚠️ **`run_command` không sandbox** — đúng cho học tập, không phù hợp production.

### 2.7 Batch Scheduler — `internal/inference/batch_scheduler.go`

Lõi của "continuous batching" (hiện là **static batch**). Gom request đến gần nhau vào 1 forward pass.

```
Handler 1 ──┐
Handler 2 ──┼──► submitCh (cap 100) ──► collectorLoop
Handler 3 ──┘        │                        │
                     │  100ms window           │ gRPC BatchGenerate
                     │  hoặc max batch (4)     │
                 events channels ◄─────────────┘  route theo request_id
```

**`collectorLoop`:** block chờ request đầu; gom thêm trong `batchWindow` (**100ms**) hoặc đến `maxBatchSize` (**4**); dispatch batch trong goroutine mới.

**`dispatchBatch`:** build `BatchGenerateRequest` + index `request_id → batchItem`; mở gRPC stream; route event về đúng channel; `final` → đóng channel; lỗi stream → `STOP_ERROR` cho request dở dang.

**Backpressure (A5):** `TrySubmit` trả `ErrOverloaded` khi queue đầy → handler trả 503 (non-stream) hoặc frame `OVERLOADED` (SSE), tăng metric `serving_overloaded_total`.

Hằng số: `DefaultBatchWindow = 100ms`, `DefaultMaxBatchSize = 4`.

### 2.8 gRPC Client — `internal/inference/client.go`

- Giữ cả 2 stubs: `InferenceServiceClient` + `BatchInferenceServiceClient`.
- Config: `insecure` credentials (local), `MaxCallRecvMsgSize = 100MB`, `MaxCallSendMsgSize = 10MB`.
- `GenerateStream`: internal → proto, mở server-streaming `Generate`, đọc trong goroutine. `io.EOF` = hết; `ctx.Err() != nil` → `STOP_CANCELLED`; lỗi khác → `STOP_ERROR`.
- `BatchGenerate`: gọi thẳng RPC batch.

> ⚠️ Runtime hiện chỉ dùng **đường batch** (loop gọi `scheduler.Submit`). Đường single `Generate` tồn tại nhưng chưa dùng ở runtime — xem §10.1.

### 2.9 Events — `internal/events/`

- Envelope (§6.2): `{event_id, event_type, event_version, timestamp, tenant_id, resource_id, trace_id, payload}`, `event_version = 1`, timestamp UTC.
- Topics: `serving.deployment.events`, `serving.inference.events`, `serving.audit.events`. Hiện dùng deployment topic.
- Deployment event types: `deployment_created`, `deployment_ready`, `deployment_failed`, `deployment_stop_requested`, `deployment_stopped`.
- Hai implementation: `KafkaEventBus` (segmentio/kafka-go, partition key `resource_id`) và `MemoryEventBus` (in-process, dùng cho test + fallback khi Kafka down).
- **Kafka tùy chọn khi boot**: nếu không kết nối được → warn + MemoryEventBus → deployment worker tắt, deployment kẹt `PENDING`; chat/inference vẫn chạy.

### 2.10 Runtime adapter + Deployment worker — `internal/runtime/`

- **`ServingRuntimeAdapter`** (§3.3 spec): `Create/Start/Stop/Restart/Delete/GetStatus/HealthCheck`. Adapter đầu tiên: `WorkerAdapter` (wrap Python worker gRPC; lifecycle validate spec + TCP health check).
- **`ComputeProvider`**: `RequestCapacity/ReleaseCapacity/GetWorkloadStatus/UpdateWorkload`. Dev dùng `MockComputeProvider` (in-memory workload ref).
- **`Worker`**: consumer của `serving.deployment.events`. `deployment_created` → chạy state machine `PENDING→PROVISIONING→STARTING→READY` (hoặc `FAILED`), set workload_ref, tạo endpoint, tạo revision, publish `deployment_ready`/`deployment_failed`. `deployment_stop_requested` → `STOPPING→STOPPED`. Idempotent nhờ state guard (event replay an toàn).
- API `POST /api/v1/deployments` trả **202 Accepted**; worker chạy async.

### 2.11 Rate limit — `internal/ratelimit/`

- Interface `Limiter`: `Allow(key, limit, window)` (fixed-window counter) + `Acquire/Release(key, limit)` (concurrency).
- `RedisLimiter`: dùng Redis (`redis/go-redis/v9`).
- Áp ở inference gateway: `tenant:{id}:rpm` + `deployment:{id}:concurrency`. **Fail-open** khi Redis down.

### 2.12 Observability — `internal/observability/`

- **Logging:** `slog` JSON structured.
- **Metrics (Prometheus, `/metrics`)**:
  - `serving_requests_total{tenant,deployment,model,region,status}`
  - `serving_request_duration_seconds{...}`
  - `serving_tokens_total{tenant,model,type}` (prompt/completion)
  - `serving_inflight_requests`, `serving_overloaded_total{tenant,model}`
- **Tracing:** span kiểu W3C `traceparent` (HTTP → agent.loop → inference.batch) emit dạng structured log, dependency-free (`trace.go`). Tiền thân của OTel.
- Route labels (tenant/deployment/model/region) được handler set sau khi routing qua `RouteLabelSetter` (statusRecorder).

### 2.13 Reliability (A5) — `internal/retry/`, `internal/circuitbreaker/`

- **Retry/backoff/jitter** (`retry`): áp dụng cho worker provisioning (RequestCapacity, adapter.Start).
- **Circuit breaker** (`circuitbreaker`): 3-state, gắn vào provisioning.
- **Idempotency**: header `Idempotency-Key` trên create deployment + bảng `idempotency_keys`.
- **Backpressure/load shedding**: BatchScheduler `TrySubmit` → `ErrOverloaded` → 503.

### 2.14 DB + migrations — `internal/infrastructure/database/`

- `database.Open` mở GORM (Postgres driver) + `Ping`; `database.Migrate` chạy gormigrate `Up` (bảng `schema_migrations`).
- **goose adoption:** DB đã migrate bằng goose trước đây (`goose_db_version`) được đánh dấu tương đương rồi bỏ qua — không chạy lại DDL trên dữ liệu hiện hữu.
- Migrations: `internal/migrations/NNNN_*.go` (chuyển 1:1 từ goose, giữ nguyên tên bảng/cột).
- Data access qua repository interface (`controlplane.Repositories`, `session.Store`); service không thấy `*gorm.DB`.
- **Bảng:** `tenants`, `users`, `tenant_memberships`, `api_keys`, `tenant_quotas`, `models`, `model_versions`, `serving_templates`, `serving_template_versions`, `deployments`, `deployment_revisions`, `endpoints`, `idempotency_keys`, `sessions`, `messages`, `usage_events`.

---

## 3. Thành phần Python Worker

Entry point: `python-worker/worker/server.py` (`python -m worker.server`). gRPC `asyncio` server, mặc định port **50051**.

### 3.1 gRPC Server — `server.py`

- **`InferenceServicer.Generate`**: proto → dict; ghép system prompt; `cancel_event` + task `watch_cancel` poll `context.cancelled()` mỗi **100ms**; stream events từ engine; lỗi → final `STOP_ERROR`.
- **`BatchInferenceServicer.BatchGenerate`**: lazy tạo `BatchEngine`; chuyển batch → dict; stream từng `(request_id, event)`; lỗi → `STOP_ERROR` cho từng request.
- Bootstrap: load engine, register 2 servicer, giới hạn message 100MB/10MB, keepalive 30s/10s, graceful shutdown (SIGINT/SIGTERM → `server.stop(5)` + `engine.unload()`).

### 3.2 InferenceEngine (single) — `engine.py`

- `MODEL_ID = "Qwen/Qwen2.5-Coder-7B-Instruct"` — **ungated**, không cần HF login.
- Config 4-bit NF4 (bitsandbytes), `device_map="auto"` — khít 12GB VRAM RTX 3060.
- `generate()`: build prompt bằng `apply_chat_template(messages, tools=...)`; `model.generate(..., streamer=TextIteratorStreamer)` trong daemon thread; đọc token → yield; kiểm tra `cancel_event` giữa các token; xác định `stop_reason` + `usage`.
- Phát hiện tool call là **heuristic** (marker trong text) — sẽ thay bằng parse đúng khi tự viết sampling/tokenizer.

### 3.3 BatchEngine — `batch_engine.py`

`generate_batch(requests)`: build prompt từng request; tokenize `padding=True, truncation=True, max_length=8192`; `model.generate(..., streamer=BatchTokenStreamer)` trong `torch.no_grad()`; **stream token theo thời gian thực** (TTFT ~0.8s), request gặp EOS/max cắt sớm độc lập; split output theo `attention_mask`; cuối mỗi request → `STOP_MAX_TOKENS`/`STOP_END_TURN` + `usage`.

> ⚠️ **BatchEngine (transformers) không phát hiện tool call** — chỉ `STOP_END_TURN`/`STOP_MAX_TOKENS`. Trên engine llama, `LlamaBackend` xử lý tool call native (§10.1).

### 3.4 LlamaBackend — `engines/llama/`

Engine llama chạy GGUF qua **llama-server** (llama.cpp): worker spawn subprocess và proxy gRPC → OpenAI-compatible HTTP. Chọn engine qua `get_backend(engine_name, ...)`.

- **`EngineBackend`** (`engines/base.py`): interface chung — `generate`/`generate_batch` yield event dict (token/tool_use/final). `get_backend()` là registry.
- **`TransformersBackend`** (`engines/transformers.py`): wrap `InferenceEngine` + `BatchEngine`.
- **`LlamaServer`** (`engines/llama/server.py`): spawn `llama-server` (`--host 127.0.0.1 --port 8081 --n-gpu-layers -1 --ctx-size 8192 --threads 8`), chờ `/health`, log file riêng, stop khi worker tắt.
- **`LlamaClient`** (`client.py`): proxy → `POST {base_url}/v1/chat/completions` (SSE), httpx.
- **`LlamaBackend`** (`backend.py`): map gRPC ↔ OpenAI body (`"model": "qwen3.5-9b"`, `tools`, `tool_choice:"auto"`); **tool calling native** → `STOP_TOOL_USE` + `tool_calls` thật.

**GGUF / binary:** `models/Qwen3.5-9B-Q4_K_M.gguf` + `models/llama.cpp/llama-server.exe`. Flags: `--engine llama --gguf <path> --llama-port 8081 --llama-bin <bin>`.

---

## 4. Proto Contract — `proto/inference.proto`

Hai service, cả hai **server-streaming**:

```proto
service InferenceService {
  rpc Generate(GenerateRequest) returns (stream GenerateResponse);
}
service BatchInferenceService {
  rpc BatchGenerate(BatchGenerateRequest) returns (stream BatchGenerateResponse);
}
```

- `GenerateRequest`: `request_id`, `session_id`, `messages` (internal canonical), `system_prompt`, `sampling_params`, `tools` (JSON Schema string).
- `GenerateResponse`: đa dạng theo `event_type` (`EVENT_TOKEN`/`EVENT_TOOL_USE`/`EVENT_FINAL`) — mỗi event populate field tương ứng.
- `StopReason`: `STOP_END_TURN`, `STOP_MAX_TOKENS`, `STOP_TOOL_USE`, `STOP_CANCELLED`, `STOP_ERROR`.
- `SamplingParams`: `max_tokens` (1024), `temperature` (0.7), `top_p` (0.9), `top_k` (50), `stop_sequences`.
- `BatchGenerateResponse`: như `GenerateResponse` + `request_id`.

**Codegen:** Go `protoc-gen-go-grpc` → `go-server/internal/inference/pb/`; Python `grpcio-tools` → `python-worker/worker/pb/` (`python -m worker.generate_proto`).

---

## 5. Luồng dữ liệu chi tiết

### 5.1 Request OpenAI non-stream

```
POST /v1/chat/completions  (Authorization: Bearer JWT/API key)
  → InferenceAuth → tenant_id
  → decode OpenAIRequest → validate
  → ResolveDeployment(tenant, model) → READY deployment (404 nếu không có)
  → rate limit (RPM tenant + concurrency deployment)
  → session (x-session-id hoặc uuid mới, persisted)
  → OpenAIToInternal → []session.Message + systemPrompt
  → handleOpenAINonStream
      → loop.RunStreaming(...) → scheduler.Submit → collectorLoop
        → gRPC BatchGenerate → Python BatchEngine
        → route event về channel → LoopEvent...
      → gom content: text + tool_calls
  → JSON response {choices[0].message, finish_reason, usage}
```

### 5.2 Request streaming (SSE)

Giống 5.1 nhưng mỗi `LoopEventToken`/`LoopEventReasoning` được viết ngay vào SSE + `Flush()`. Tốc độ token phụ thuộc TPOT của model (~75–80ms/token theo `docs/BENCHMARK.md`).

### 5.3 Agentic loop có tool call

```
user msg → model → model trả STOP_TOOL_USE + toolCalls
  → loop execute từng tool (LocalToolExecutor, timeout 30s)
  → lưu tool_result vào session
  → iteration kế: model nhìn lại toàn bộ history (kèm tool results) → tiếp tục
  → ... đến khi STOP_END_TURN hoặc đủ 10 iterations
```

Trên engine **transformers**, `tool_use` không bao giờ được sinh ở đường batch (§10.1). Trên engine **llama**, nhánh này **chạy thật** — verified E2E.

### 5.4 Concurrent requests (static batching)

```
Handler A, B, C đến gần nhau
  → cả 3 Submit vào submitCh
  → collectorLoop gom trong 100ms (hoặc tới batch=4)
  → 1 gRPC BatchGenerate với [A,B,C]
  → Python: 1 model.generate(batch=3)
  → stream (req_id, token) → Go route về 3 channel riêng → 3 SSE riêng biệt
```

Lợi ích (`docs/BENCHMARK.md`): throughput single ~13 tok/s → batch 4 đạt **~54 tok/s (3.8×)**.

### 5.5 Async deployment (control plane)

```
POST /api/v1/deployments (Idempotency-Key tùy chọn)
  → auth (deployment.write) → tenant từ claims
  → CreateDeployment (PENDING) → publish deployment_created
  → 202 Accepted
       │
       ▼ (Kafka / Memory bus)
  runtime.Worker consume
  → ComputeProvider.RequestCapacity → ServingRuntimeAdapter.Start
  → transition PENDING→PROVISIONING→STARTING→READY
  → set workload_ref, create revision + endpoint
  → publish deployment_ready
```

---

## 6. Cancel propagation

Chain cancel từ client đến GPU:

```
Client disconnect
  → r.Context().Done()
  → handler: goroutine gọi cancel()
  → ctx truyền vào loop.RunStreaming
  → loop check ctx.Done(); scheduler select ctx.Done() khi route event
  → gRPC stream context cancel → Python watch_cancel (poll 100ms) set cancel_event
  → engine.generate: kiểm tra cancel_event giữa các token → yield STOP_CANCELLED
  → stream kết thúc, VRAM giải phóng
```

Giới hạn: cancel phía Python là **poll 100ms**; trong batch mode model vẫn chạy hết forward pass của batch (chỉ bỏ gửi kết quả request bị cancel).

---

## 7. Batching & mô hình concurrency

| Khía cạnh | Thiết kế hiện tại |
|---|---|
| Đơn vị dispatch | Batch tĩnh: gom trong 100ms hoặc đủ 4 request |
| GPU thực thi | `model.generate()` với batched inputs (padding + attention mask) |
| Routing | Go giữ `request_id → channel` map, route từng event |
| Backpressure | `submitCh` cap 100; `TrySubmit` → `ErrOverloaded` → 503/OVERLOADED |
| Dynamic batching | **Chưa** — không chèn/xoá sequence giữa các decode step (Tuần 7-8) |
| Multi-user | Session bền theo tenant/user + auth |
| Channel buffer | Loop events: 64; gRPC events: 100 |

> **Lưu ý:** `BatchEngine` chạy `model.generate()` cho cả batch và stream kết quả sau khi forward pass hoàn tất; các request trong batch xếp hàng tại Python. Đây là static batching, không phải true continuous batching.

---

## 8. Model & inference

- **Model (transformers, default):** Qwen2.5-Coder-7B-Instruct, quant 4-bit NF4, `device_map="auto"` (RTX 3060 12GB).
- **Model (llama):** Qwen3.5-9B, GGUF Q4_K_M (`models/Qwen3.5-9B-Q4_K_M.gguf`), chạy qua llama-server (llama.cpp, CUDA 12.4), `--n-gpu-layers -1`.
- **Tokenizer:** BPETokenizer tự viết (`worker/model/tokenizer/bpe.py` — byte-level BPE). Load `vocab.json`/`merges.txt`/`tokenizer_config.json` của Qwen (IDs khớp 100%), tự implement byte-encoder, regex pre-tokenization, BPE merge, decode (kể cả `StreamingDecoder`), batch pad/truncate. Đảm nhận encode/decode trong `engine.py` lẫn `batch_engine.py`. Test đối chiếu ID == HF (`tests/test_tokenizer.py`).
- **Chat template:** `hf_tokenizer.apply_chat_template` — chỉ dùng HF `AutoTokenizer` cho Jinja template (build prompt string), không token hoá. Hỗ trợ tool calling.
- **Streaming:** `TextIteratorStreamer` + daemon thread; `BPETokenizer` duck-type khớp decode interface.
- **Sinh token hiện do HuggingFace đảm nhiệm** (sampling + KV cache của HF). Còn lại: Tuần 5-6 tự viết sampling, Tuần 7-8 tự quản lý KV cache + dynamic batching.
- **Token counting:** Go heuristic `chars/4`; Python đếm chính xác qua tokenizer khi trả `usage`.

Benchmark (`docs/BENCHMARK.md`): TTFT ~70–85ms, TPOT ~75–82ms, single throughput ~13 tok/s.

---

## 9. Web UI — `web/`

NextJS (App Router, React 19, Tailwind v4, TypeScript). Proxy `/api/v1/*`, `/v1/*`, `/health`, `/metrics` về Go server qua `rewrites()` (`AI_FACTORY_API_URL`, mặc định `http://localhost:8080`) → browser gọi same-origin, SSE không vướng CORS.

| Route | Nội dung |
|---|---|
| `/login` | Đăng nhập JWT |
| `/chat` | Chat SSE, markdown, model selector; sidebar lịch sử session |
| `/platform` | Usage + API Keys |
| `/infra` | Deployments (start/stop), models + versions, templates + versions, quotas |
| `/admin` | Tenants (chỉ `PLATFORM_ADMIN`) |

Auth: JWT lưu `localStorage`, decode client-side để phân role. UI tĩnh cũ (`ui/`) vẫn được Go server phục vụ cho test nhanh.

---

## 10. Hạn chế & lỗ hổng tích hợp hiện tại

### 10.1 Tool-calling: hoạt động trên engine llama, chết trên engine transformers

> ✅ **Engine llama (Qwen3.5-9B): tool-use ĐÃ HOẠT ĐỘNG và verified E2E (2026-08-10).** llama-server hỗ trợ tool calling native → `LlamaBackend` parse `tool_calls` → `STOP_TOOL_USE` + `tool_calls` thật → nhánh tool-use của `loop.go` kích hoạt. E2E đã xác nhận model gọi `read_file`, Go executor chạy, model trả lời với nội dung file.
>
> ⚠️ **Engine transformers (Qwen2.5-Coder-7B): vẫn chết.** Giới hạn dưới đây áp dụng cho đường transformers.

- Agentic loop chỉ gọi `scheduler.Submit` → `BatchGenerate` → `TransformersBackend` → `BatchEngine.generate_batch`.
- `BatchEngine` đã stream token theo thời gian thực nhưng **không bao giờ yield `tool_use`** — chỉ `token`/`final` với `STOP_END_TURN`/`STOP_MAX_TOKENS`.
- Hệ quả: trên transformers, nhánh tool-calling của `loop.go` (step 7) không kích hoạt. Model nhận `tools` trong prompt nhưng tool call bị coi là text thường.
- Cần sửa: cho `BatchEngine`/`TransformersBackend` parse tool call từ output, hoặc loop dùng `GenerateStream` (single) cho request cần tool.

### 10.2 Tool definitions từ client chưa được nối

- `adapters.go` có `ToolsToInternal` / `OpenAIToolsToInternal`, handler parse `req.Tools`, nhưng **không truyền xuống loop**.
- Loop luôn dùng `l.tools.ListTools()` — **4 built-in tools** của `LocalToolExecutor`, không phải tool client khai báo.

### 10.3 Flag `--max-concurrent` không tác dụng khi ≤ 1

- `main.go`: `SetMaxBatchSize` chỉ gọi khi `maxConcurrent > 1`; mặc định flag `1`. Batch size thực tế luôn là `DefaultMaxBatchSize = 4` cho tới khi override. Log cũng in `max_batch` sai khi flag = 1.

### 10.4 Inconsistency trong comment (model)

- Một số docstring/help string còn ghi "Llama 3.2 3B" trong khi `MODEL_ID` thực tế là Qwen2.5-Coder-7B. Comment heuristic tool-call cũ nói format Llama.

### 10.5 Khác

- **`run_command` không sandbox** — chạy trực tiếp trên host với `sh -c`.
- **Cost/quota enforcement chưa đủ**: usage được ghi (`usage_events`, Prometheus) nhưng **chưa trừ vào quota** khi vượt.
- **Persistence cho domain khác**: session/control plane đã bền; một số state worker (workload ref…) vẫn dev-only.
- Cancel Python là poll 100ms.

---

## 11. Ngăn xếp công nghệ

| Layer | Tech |
|---|---|
| Go server | Go 1.25.7, `gin-gonic/gin` v1.11, `google.golang.org/grpc`, `google/uuid` |
| Persistence | PostgreSQL qua `gorm.io/gorm` + `gorm.io/driver/postgres`, migrate bằng `go-gormigrate/gormigrate/v2` |
| Rate limit | Redis (`redis/go-redis/v9`) |
| Events | Kafka (`segmentio/kafka-go`) hoặc in-memory fallback |
| Auth | `golang-jwt/jwt/v5`, argon2/bcrypt, API key hash |
| Observability | `prometheus/client_golang`, `log/slog` JSON, W3C trace tự viết |
| Reliability | `internal/retry`, `internal/circuitbreaker` (tự viết) |
| Python worker | Python ≥3.11, `grpcio` (aio), `torch`, `transformers`, `bitsandbytes`, `accelerate` (+ `httpx` cho llama proxy) |
| Model | Transformers: Qwen/Qwen2.5-Coder-7B-Instruct (4-bit NF4) · Llama: Qwen3.5-9B GGUF Q4_K_M (llama-server) |
| Contract | Protobuf 3, server-streaming gRPC |
| Streaming | gRPC (Python→Go), SSE (Go→Client) |
| Web UI | Next 16 (App Router), React 19, Tailwind v4, TypeScript |

---

## 12. Cách chạy

```bash
# Hạ tầng (Postgres bắt buộc; Kafka/Redis tùy chọn cho full flow)
docker compose -f deployments/docker-compose.yml up -d postgres kafka redis

# Terminal 1: Python worker (mặc định port 50051) — engine transformers (default)
cd python-worker && python -m worker.server

#   ... hoặc engine llama (Qwen3.5-9B GGUF): spawn llama-server trên port 8081
cd python-worker && python -m worker.server --engine llama --gguf ..\models\Qwen3.5-9B-Q4_K_M.gguf

# Terminal 2: Go server (mặc định port 8080)
#   Bắt buộc có Postgres; server fail boot nếu không kết nối được.
cd go-server && go run ./cmd/server/

# Terminal 3: NextJS UI (mặc định port 3000)
cd web && npm install && npm run dev

# Test nhanh — inference endpoint yêu cầu auth
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'

# Health
curl http://localhost:8080/health
```

---

## 13. Map với learning roadmap

| Giai đoạn | Nội dung | Trạng thái trong code |
|---|---|---|
| Tuần 1-2 | End-to-end: proto → gRPC → Go → model | ✅ Đã xong |
| Tuần 1-2 | OpenAI protocol + SSE + agentic loop | ✅ Đã xong (tool-calling còn lỗ hổng, §10.1) |
| Tuần 1-2 | Continuous batching (static) | ✅ Đã xong (static batch) |
| Tuần 3-4 | Tự viết tokenizer (BPE) | ✅ Đã xong — `worker/model/tokenizer/` |
| Bổ sung | Engine llama (Qwen3.5-9B GGUF, llama-server proxy) | ✅ Đã xong — tool calling E2E |
| M1–M3 | Control plane + auth + async deploy + routing/rate limit | ✅ Đã xong |
| A5 | Reliability: retry, circuit breaker, idempotency, backpressure | ✅ Đã xong |
| A6 | Observability: metrics, trace, structured log, usage metering | ✅ Đã xong |
| UI | NextJS app (`web/`) + chat history + platform console | ✅ Đã xong |
| Tuần 5-6 | Tự viết sampling | ⏸️ Tạm hoãn — ưu tiên tái kiến trúc (xem spec 2026-09-11) |
| Tuần 7-8 | Tự quản lý KV cache + dynamic batching | 🔜 Chưa |
| Tuần 9+ | Forward pass, prefix caching, PagedAttention | 🔜 Chưa |

---

## 14. Kế hoạch tái kiến trúc (modular monolith)

Go server sẽ được cấu trúc lại theo production blueprint (`00-overview.md`): **modular monolith + DI container + composition root + multi-binary**, đổi stack sang **Gin + GORM + gormigrate + viper + zap**. Python worker vẫn là data plane (không đụng tới).

- Spec: [`docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`](superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md)
- Plan Phase 1: [`docs/superpowers/plans/2026-09-11-phase1-composition-root-di.md`](superpowers/plans/2026-09-11-phase1-composition-root-di.md)
- Lộ trình: P1 nền tảng (DI + composition root) → P2 GORM/gormigrate + repository → P3 Gin → P4 tách `services/*` → P5 multi-binary → P6 outbox/cache-aside.
