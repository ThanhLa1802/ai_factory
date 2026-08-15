# Design — M3: Inference gateway routing (model → deployment) + rate limit

- **Ngày**: 2026-08-15
- **Trạng thái**: Đã duyệt qua brainstorming (user duyệt design + 5 quyết định) — chờ review spec trước khi lập kế hoạch
- **Phạm vi**: Nối đường inference `/v1/chat/completions` vào control plane: request `model` resolve thành deployment READY của tenant (tenant isolation thực sự ở data plane) + thêm rate limit (tenant RPM + deployment concurrency, backend Redis). Đây là "M3" mà `runtime/worker.go:193` đã đặt chỗ ("a real inference gateway resolves this in M3").
- **Liên hệ spec nền**: xây trên `2026-08-15-serving-platform-design.md` (M2 runtime adapter) và `2026-08-15-consumer-auth-ui-design.md` (auth inference). M3 front-load phần **routing + rate limit** của Phase 4 trong README platform spec (§10 Inference Gateway, §29 Rate Limiting, §30 Inference Routing).

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng đã xác minh (từ code)

- **Đường gọi model KHÔNG resolve deployment**: `handler.go:44` đăng ký `/v1/chat/completions` qua `auth.InferenceAuth`, rồi `handleOpenAIChatCompletions` decode request → `sessionMgr` → `agent.Loop.RunStreaming` → `batchScheduler` → `inferenceClient` (gRPC) → **1 worker duy nhất** (`--inference-addr`, mặc định `localhost:50051`). Field `req.Model` chỉ dùng để echo trong response, **không** được truyền xuống loop/worker, không resolve gì.
- **Tenant từ auth chưa dùng ở data plane**: `auth.InferenceAuth` (`middleware.go:74`) đặt `Claims` (JWT) hoặc `*controlplane.APIKey` (có `TenantID`) vào context, nhưng handler không đọc tenant để enforce gì. Tenant isolation hiện chỉ ở control plane (CRUD gated theo tenant).
- **Endpoint concept có sẵn**: `controlplane.CreateEndpoint` (`deployment.go:167`) tạo endpoint path placeholder `/v1/chat/completions/{deployment_id}` khi deployment READY; `runtime/worker.go:193` `endpointPath()` ghi rõ path này là placeholder, gateway thật resolve trong M3.
- **Deployment lifecycle có sẵn**: `controlplane.Deployment` (`deployment.go:12`) có `TenantID, ModelVersionID, TemplateVersionID, Status, WorkloadRef, DesiredReplicas`. State machine (`state.go`): `PENDING → … → READY`. Deployment READY do deployment worker tạo (chỉ khi Kafka connected; không Kafka → deployments stay PENDING).
- **Model registry có sẵn**: `controlplane.Model{Name, …}`, `ModelVersion{ModelID, Version, …}` (`model.go`); chưa có method resolve model name → deployment.
- **Quota table có sẵn** (`quota.go`): `tenant_quotas(tenant_id, quota_type, limit_value, period)` — nhưng chỉ là config/billing, **chưa enforce**.
- **Metrics**: `serving_requests_total` + `serving_request_duration_seconds` (`observability/metrics.go`), nhưng labels `tenant/deployment/model/region` **đang để trống** — `main.go:189` ghi rõ "chưa resolve ở tầng HTTP".
- **`controlplane.ErrNotFound` đã có** (`users.go`), dùng lại.
- **Wiring** (`cmd/server/main.go`): `api.NewHandler(sessionMgr, loop, dir, authSvc, secret)` (dòng 126) đã nhận `authSvc` + secret; **chưa nhận** resolver/limiter. `cp := controlplane.NewService(...)` đã có sẵn trong main.
- **`go.mod`**: dùng `segmentio/kafka-go`; **chưa có Redis client** — cần thêm `github.com/redis/go-redis/v9`.

### 1.2 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Routing scope = logical routing + tenant isolation** — resolve request `model` thành deployment READY của tenant, validate tenant có quyền, route đến worker (vẫn 1 address global). **Không** multi-replica / health-aware routing (chỉ 1 GPU 12GB). |
| D2 | **Resolve theo model name**: request `model` = `Model.name` trong registry → `model_versions` của deployment → deployment READY (tenant match). Không tìm thấy → **404 RESOURCE_NOT_FOUND** (fail closed). Không resolve qua endpoint path. |
| D3 | **Rate limit backend = Redis** (`go-redis/v9`), thêm `redis` vào docker-compose. |
| D4 | **Redis down → fail open**: log warning + cho request đi qua (rate limit là lớp bảo vệ thêm, không phải lối mòn — cùng tinh thần Kafka hiện tại: unreachable → fallback). |
| D5 | **Rate limit MVP = tenant RPM + deployment concurrency**. `tpm` / api-key / endpoint levels → defer (YAGNI). |
| D6 | **Fail closed khi không có deployment READY** → 404 (đúng README §43 error model). Hệ quả: quick test cần seed deployment READY (D8). |
| D7 | **Bonus — điền metrics labels** `tenant/deployment/model/region` (đang trống) sau khi resolve: handler set tenant/deployment/model vào request context, `metricsMiddleware` (main.go) đọc context để điền labels. Optional — cắt nếu phức tạp. |
| D8 | **Seed demo data** (model `qwen-3b` + version + deployment READY) để E2E routing hoạt động không cần Kafka. |

---

## 2. Kiến trúc

```
Client → POST /v1/chat/completions  (Bearer JWT|api-key, body.model = name)
   │
   ▼
auth.InferenceAuth(secret, authSvc)          [đã có]  → Claims (JWT) | APIKey (tenant)
   │
   ▼
handleOpenAIChatCompletions                  [đã có]
   ├─ decode + ValidateOpenAIRequest          [đã có]
   ├─ tenantID := TenantIDFromContext(ctx)    [MỚI]  (JWT → Claims.TenantID | key → APIKey.TenantID)
   ├─ d := cp.ResolveDeployment(ctx, tenantID, req.Model)   [MỚI]
   │      model name → active version → deployment READY (tenant match)
   │      không có → 404 {"error":{"code":"RESOURCE_NOT_FOUND",...}}
   ├─ limiter.Allow(ctx, "tenant:"+tenantID+":rpm", rpmLimit, 1m)   [MỚI]
   │      quá → 429 {"error":{"code":"RATE_LIMIT_EXCEEDED",...}}
   ├─ limiter.Acquire(ctx, "deployment:"+d.ID+":concurrency", concLimit)  [MỚI]
   │      quá → 429; defer limiter.Release(...)
   │      Redis down (cả Allow lẫn Acquire) → log warning + cho qua (D4)
   └─ agent.Loop.RunStreaming → batchScheduler → inferenceClient (worker global, 1 address)
```

---

## 3. Routing — `controlplane.ResolveDeployment`

**File**: `go-server/internal/controlplane/deployment.go` (cạnh `GetDeployment`/`ListDeployments`).

```go
// ResolveDeployment trả deployment READY mới nhất của tenant serve model `modelName`.
// Lỗi ErrNotFound nếu không có model/version/deployment READY khớp.
func (s *Service) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*Deployment, error) {
    // SELECT d.*
    // FROM deployments d
    // JOIN model_versions mv ON mv.id = d.model_version_id
    // JOIN models m        ON m.id  = mv.model_id
    // WHERE m.name = $1 AND d.tenant_id = $2 AND d.status = 'READY'
    // ORDER BY d.created_at DESC LIMIT 1
}
```

- Trả `ErrNotFound` khi `pgx.ErrNoRows` — handler map sang 404.
- Lấy deployment mới nhất (`ORDER BY created_at DESC`) khi tenant có nhiều deployment cùng model — MVP không cần chọn replica phức tạp.
- Dùng hằng `controlplane.DeploymentReady` (đã có) thay cho literal `'READY'`.

**Tenant từ context** — thêm helper trong `auth/middleware.go` (đối xứng `ClaimsFromContext`/`APIKeyFromContext`):

```go
// TenantIDFromContext trả tenant từ Claims (JWT) hoặc APIKey, bất kể đường auth nào.
func TenantIDFromContext(ctx context.Context) (string, bool)
```

---

## 4. Rate limit — package `internal/ratelimit`

**Files**: `go-server/internal/ratelimit/limiter.go` (interface) + `go-server/internal/ratelimit/redis.go` (Redis impl).

```go
type Limiter interface {
    // Allow kiểm tra fixed-window counter cho key dưới limit trong window.
    // true = được phép. Lỗi (Redis down) → caller quyết fail-open.
    Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)

    // Acquire tăng concurrency counter cho key; false nếu vượt limit.
    Acquire(ctx context.Context, key string, limit int) (bool, error)

    // Release giảm concurrency counter cho key (best-effort).
    Release(ctx context.Context, key string) error
}
```

**`RedisLimiter`** (go-redis/v9):
- `Allow` (RPM): `INCR key`; nếu giá trị vừa `== 1` → `EXPIRE key window` (fixed window). Trả `count <= limit`.
- `Acquire` (concurrency): `INCR key`; nếu `count > limit` → `DECR key` (rollback) rồi trả `false`, ngược lại `true`.
- `Release`: `DECR key` (floor 0, best-effort, không trả lỗi nghiêm trọng).

**Fail-open** (D4): handler bọc gọi `Allow`/`Acquire` trong helper — nếu `err != nil` → `log` warning + coi như được phép (không chặn). Chỉ `false` thật (không lỗi) mới trả 429.

**Limits** từ config (không đọc `tenant_quotas` — defer): `AI_FACTORY_RATE_LIMIT_RPM` (default 60), `AI_FACTORY_RATE_LIMIT_CONCURRENCY` (default 4, khớp `max_batch`).

---

## 5. Config & wiring

**`config/config.go`** — thêm 3 trường + env:
- `RedisAddr string` — `AI_FACTORY_REDIS_ADDR` (default `localhost:6379`).
- `RateLimitRPM int` — `AI_FACTORY_RATE_LIMIT_RPM` (default `60`).
- `RateLimitConcurrency int` — `AI_FACTORY_RATE_LIMIT_CONCURRENCY` (default `4`).

**`cmd/server/main.go`**:
- Khởi tạo `redisClient := redis.NewClient(...)` + `limiter := ratelimit.NewRedisLimiter(redisClient)`.
- Truyền `cp` (resolver) + `limiter` + limits vào `api.NewHandler(...)`.

**`api/handler.go`**:
- `Handler` thêm field `resolve *controlplane.Service` + `limiter ratelimit.Limiter` + `rpmLimit, concLimit int`.
- `NewHandler(...)` nhận thêm các dependency này.

**`deployments/docker-compose.yml`** — thêm service `redis` (image `redis:7-alpine`, port `6379`).

**`go.mod`** — thêm `github.com/redis/go-redis/v9`.

---

## 6. API surface / error model (thay đổi)

| Route | Thay đổi |
|---|---|
| `POST /v1/chat/completions` | **Mới**: resolve deployment + rate limit trước khi chạy loop. Lỗi mới: `404 RESOURCE_NOT_FOUND` (không có deployment READY cho model+tenant), `429 RATE_LIMIT_EXCEEDED` (quá rpm/concurrency). |

Error shape dùng đúng `{"error":{"code":...,"message":...}}` (đã có `writeOpenAIError`/`writeAPIError`). Không log token/key/prompt.

---

## 7. Scope cut / non-goals (chốt rõ)

- ❌ Không multi-replica / health-aware routing / load balancing (1 worker, D1).
- ❌ Không resolve qua endpoint path `/v1/chat/completions/{deployment_id}` (D2) — path vẫn là placeholder, defer.
- ❌ Không `tpm`, không api-key/deployment/endpoint-level rate limit (D5).
- ❌ Không enforce `tenant_quotas` table (config tách rời, defer).
- ❌ Không sửa proto / không đụng Python worker (contract inference giữ nguyên).
- ⚠️ **Hệ quả chấp nhận (D6 + D8)**: quick test `curl` phải có deployment READY của tenant; seed demo cung cấp sẵn. Nếu người dùng gọi model không có trong registry (hoặc chưa READY) → 404.

---

## 8. Testing

1. **`controlplane/deployment_test.go`** — `ResolveDeployment` (gate `AI_FACTORY_DATABASE_URL`, theo mẫu test deployment hiện có):
   - Model + version + deployment READY khớp tenant → trả đúng deployment.
   - Model khác tenant → `ErrNotFound` (tenant isolation).
   - Deployment không ở READY (PENDING/STOPPED) → `ErrNotFound`.
   - Model name không tồn tại → `ErrNotFound`.
   - Nhiều deployment cùng model → trả mới nhất.
2. **`ratelimit/redis_test.go`** — `RedisLimiter` với `miniredis` (test local, không cần Redis thật):
   - `Allow`: dưới limit → true; vượt limit trong window → false; window mới (qua thời gian) → reset.
   - `Acquire`/`Release`: acquire tới limit → true; vượt → false (rollback); release giảm counter.
   - Redis down (địa chỉ sai) → trả error (để handler fail-open).
3. **`auth/middleware_test.go`** — `TenantIDFromContext`: JWT → tenant; API key → tenant; không auth → false.
4. **`api/handler_test.go`** — E2E handler với resolver mock + limiter mock (interface, không cần Redis/DB):
   - Không có deployment READY → 404.
   - Rate limit vượt → 429.
   - Fail-open: limiter trả error → request vẫn chạy vào loop (mock loop).
5. **`go vet ./...`** + `go test ./...` xanh (test Postgres/Redis-gated skip khi không có service).

---

## 9. Files touched

- `go-server/internal/controlplane/deployment.go` — thêm `ResolveDeployment`.
- `go-server/internal/controlplane/deployment_test.go` — test resolve.
- `go-server/internal/ratelimit/limiter.go` — interface `Limiter` + key helper.
- `go-server/internal/ratelimit/redis.go` — `RedisLimiter` (go-redis/v9).
- `go-server/internal/ratelimit/redis_test.go` — test với miniredis.
- `go-server/internal/auth/middleware.go` — thêm `TenantIDFromContext`.
- `go-server/internal/auth/middleware_test.go` — test helper.
- `go-server/internal/api/handler.go` — inject resolver + limiter; resolve + rate limit trong `handleOpenAIChatCompletions`; (bonus D7) điền metrics labels.
- `go-server/internal/api/handler_test.go` — E2E resolve/rate-limit/fail-open.
- `go-server/internal/config/config.go` + `config_test.go` — thêm RedisAddr + limits.
- `go-server/cmd/server/main.go` — wiring redis client + limiter + limits.
- `go-server/cmd/server/seed.go` — seed demo model `qwen-3b` + version + deployment READY (D8).
- `go-server/go.mod` / `go.sum` — thêm `go-redis/v9` (+ miniredis test dep).
- `deployments/docker-compose.yml` — thêm service redis.
- `CLAUDE.md`, `docs/TRACKING.md` — cập nhật (routing + rate limit đã có; gỡ dòng "chưa có: rate-limit" khỏi việc treo).
