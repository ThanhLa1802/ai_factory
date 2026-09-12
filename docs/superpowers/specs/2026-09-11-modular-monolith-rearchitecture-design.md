# Design — Kiến trúc lại AI Factory theo production blueprint (modular monolith)

- **Ngày**: 2026-09-11
- **Trạng thái**: Đã chốt qua brainstorming — chờ review spec trước khi lập plan chi tiết từng phase
- **Nguồn tham chiếu**: `00-overview.md` — *AI Marketplace API Services* (reimplementation blueprint, **chỉ có chương 00**; các chương 01–07 được nhắc nhưng không có trong tay)
- **Phạm vi**: Kiến trúc lại **Go server** của AI Factory theo pattern production (modular monolith + DI + composition root + multi-binary) và **đổi stack** HTTP/ORM/config/logging sang Gin + GORM + gormigrate + viper + zap. **Giữ nguyên** Python worker + gRPC + proto (data plane), agentic loop, tool executor, session, SSE.

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng AI Factory (đã xác minh)

- **Go server**: `net/http` + `ServeMux`, truy cập Postgres bằng `pgx` trực tiếp, migrate bằng `goose`, log bằng `log/slog`, config đọc từ env.
- **Cấu trúc**: `internal/*` **phẳng** (14 package), không có service module; `internal/controlplane` gộp users + catalog + deployment + quota + usage vào một package.
- **Wiring**: toàn bộ trong `cmd/server/main.go` (259 dòng), worker chạy **in-process** cùng API server; chỉ có **một binary** `cmd/server`.
- **Đã có sẵn (đừng đánh mất)**: control plane persistence, auth (JWT + API key + RBAC), runtime adapter + deployment worker + Kafka events, rate limit (Redis), observability (Prometheus metrics + W3C trace + slog JSON), circuit breaker, retry, idempotency, chat history + usage metering.
- **Python worker**: data plane thật (2 engine, tokenizer BPE tự viết, gRPC server-streaming). Đây là mục tiêu học chính (roadmap Tuần 5–9) → **không đụng tới**.

### 1.2 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Phạm vi A** — cấu trúc lại Go server theo pattern prod, **giữ** Python worker làm data plane. |
| D2 | **Modular monolith + DI + composition root** — một binary logic, nhiều service module, wiring tập trung. |
| D3 | **Multi-binary** `cmd/{server,worker,migrate,seed}` — cùng codebase, config chọn vai trò. |
| D4 | **Đổi stack** sang **Gin + GORM + gormigrate** (như prod). |
| D5 | **Config** sang **viper** (+ `configs/config.yaml`, env override). |
| D6 | **Logging** sang **zap + lumberjack** (quyết định bridge — xem §6.4). |
| D7 | **Không** thêm Kong/proxy-server, KServe/K8s, ClickHouse, Keycloak, billing đầy đủ trong đợt này (khác prod — vì ai_factory là learning project). |
| D8 | **Tạm hoãn sampling loop (roadmap Tuần 5–6)** — ưu tiên hoàn tất đợt tái kiến trúc trước, quay lại inference sau. |

### 1.3 Điểm mâu thuẫn cần nhớ

Blueprint coi inference là hộp đen bên ngoài (KServe). AI Factory **tồn tại để tự viết inference engine**. Vì vậy mục tiêu là **sao chép *pattern kiến trúc*** của prod ở tầng control plane Go, **không** sao chép việc đẩy inference ra ngoài.

---

## 2. Nguyên tắc từ blueprint áp dụng

1. **Một binary, nhiều service module** — mỗi module tự trị `router/handlers/services/repositories/models/dto`.
2. **Composition root** — `internal/app` giữ toàn bộ wiring; **cross-service chỉ nối ở đây**, không import chéo giữa `services/*`.
3. **DI container tự viết** (`pkg/di`) — lazy singleton, lifecycle (`Shutdown`/`Close`), phát hiện circular dependency.
4. **Service bật/tắt bằng config** — cùng image chạy nhiều vai trò (API node / worker node).
5. **Repository pattern + interface ở mọi seam** — mock được trong unit test.
6. **Event tự động từ DB write + transactional outbox** — không rải publish thủ công trong handler.
7. **Cache-aside + distributed lock trên Redis** cho dữ liệu đọc nhiều và thao tác chỉ chạy một lần.
8. **Aggregate trước, ghi DB sau** — usage cộng dồn ở Redis, flush định kỳ.
9. **Control plane / data plane tách rời** — giữ nguyên gRPC boundary hiện có.
10. **Tiền dùng `decimal`, không float** (khi thêm billing).
11. **Middleware global theo chuỗi cố định** — request-id → logger → recovery → CORS → security → auth.

---

## 3. Gap analysis (AI Factory hiện tại ↔ blueprint)

| Blueprint | AI Factory hiện tại | Xử lý |
|---|---|---|
| `internal/services/*` (modular) | `internal/*` phẳng; `controlplane` gộp | Phase 4 |
| DI container `pkg/di` | `main.go` wiring thủ công | Phase 1 |
| Composition root `internal/app` | `main.go` | Phase 1 |
| Nhiều binary `cmd/{server,workers,migrate,seed}` | Chỉ `cmd/server`, worker in-process | Phase 5 |
| Layer `router/handlers/services/repositories/models/dto` | `api/` + `controlplane` gộp | Phase 2 + 4 |
| Repository + interface mọi seam | pgx gọi trực tiếp | Phase 2 |
| GORM + gormigrate, Gin, zap, viper | pgx + goose, net/http, slog, env | Phase 1–3 |
| Event auto từ DB write + outbox | `events` publish thủ công; chưa có outbox | Phase 6 |
| Cache-aside + distributed lock | Chỉ rate-limit Redis | Phase 6 |
| Usage aggregate Redis → flush | Ghi `usage_events` mỗi turn | Phase 6 |
| Control plane / data plane tách | Đã tách (Go ↔ Python qua gRPC) | Giữ |
| `pkg/` dùng chung, không phụ thuộc nghiệp vụ | Chưa có `pkg/` | Phase 1+ |
| i18n/currency/timezone middleware, ClickHouse, decimal | Không có | Non-goal (§11) |

---

## 4. Kiến trúc đích

### 4.1 Cây thư mục

```
go-server/
├── cmd/
│   ├── server/main.go          # API node — mỏng: config → app.New → Run
│   ├── worker/main.go          # deployment worker (tách khỏi server)
│   ├── migrate/main.go         # gormigrate runner
│   └── seed/main.go            # seeder runner
├── configs/
│   └── config.yaml             # config mặc định (viper); env override
├── internal/
│   ├── app/                    # composition root
│   │   ├── app.go              # App lifecycle: Run() + Shutdown()
│   │   ├── registry.go         # RegisterAll(container, cfg) — 5 nhóm
│   │   └── seeder.go           # seedAdmin / seedDemo
│   ├── config/                 # viper loader + struct
│   ├── infrastructure/
│   │   ├── database/           # GORM + postgres driver; gormigrate wiring
│   │   ├── cache/              # Redis client + cache-aside + distributed lock
│   │   ├── message/            # Kafka producer/consumer (chuyển từ internal/events)
│   │   ├── outbox/             # transactional outbox
│   │   ├── events/             # event envelope + publisher + GORM hook
│   │   ├── middleware/         # gin middleware: requestid/logger/recover/cors/security
│   │   ├── observability/      # zap logger, metrics, trace
│   │   ├── circuitbreaker/ retry/
│   │   └── inference/          # gRPC client + batch scheduler (data-plane client)
│   ├── services/
│   │   ├── iam/                # auth, users, tenants, api keys, RBAC
│   │   ├── serving/            # catalog model/version, template, deployment, runtime adapter
│   │   ├── inference/          # chat completions, agentic loop, sessions, tools
│   │   └── usage/              # usage metering + quota
│   └── migrations/             # gormigrate, tách theo service
└── pkg/
    ├── di/                     # DI container (lazy singleton + lifecycle + circular detect)
    ├── response/               # envelope JSON thống nhất
    └── ...
```

### 4.2 Layer rule (bắt buộc cho mọi service module)

```
internal/services/<name>/
├── router.go        # RegisterRoutes(engine, deps) — khai báo route + middleware chain
├── handlers/        # HTTP layer: bind/validate, gọi service, format response
├── services/        # business logic, orchestration, transaction boundary
├── repositories/    # data access qua GORM; CHỈ layer này biết DB
├── models/          # GORM entity = schema DB
└── dto/             # request/response contract (tách khỏi model)
```

- Chiều phụ thuộc: `handlers → services → repositories → models`. Ngược chiều = vi phạm.
- `infrastructure/` được mọi layer gọi, **không** gọi ngược lên `services/`.
- `pkg/` không phụ thuộc nghiệp vụ.

### 4.3 Ánh xạ package hiện tại → service module

| Hiện tại | Đích |
|---|---|
| `internal/auth` | `services/iam` (dto/database) |
| `internal/controlplane` (users + catalog + deployment + quota) | tách: `services/iam` (users/tenants), `services/serving` (catalog/deployment), `services/usage` (quota/usage) |
| `internal/api` (handler + controlplane handler + SSE) | `services/inference` (chat/SSE) + router của từng service |
| `internal/agent` | `services/inference` (agentic loop + tools) |
| `internal/session` | `services/inference` (session store) |
| `internal/inference` | `infrastructure/inference` (gRPC client + batch scheduler) |
| `internal/runtime` | `services/serving` (runtime adapter + worker) |
| `internal/events` | `infrastructure/{events,message}` |
| `internal/db` | `infrastructure/database` + `migrations/` |
| `internal/ratelimit` | `infrastructure/cache` |
| `internal/observability` | `infrastructure/observability` |
| `internal/config` | `internal/config` (viper) |
| `internal/circuitbreaker`, `internal/retry` | `infrastructure/circuitbreaker`, `infrastructure/retry` |
| `internal/db/migrations/*.sql` (goose) | `internal/migrations/*.go` (gormigrate) |

### 4.4 Composition root — vòng đời khởi động

```
main()
 ├─ config.Load()                       # viper: configs/config.yaml + env override
 ├─ observability.SetupLogger(cfg.Log)  # zap + lumberjack
 ├─ di.NewContainer()
 ├─ app.RegisterAll(container, cfg)     # 5 nhóm, theo thứ tự phụ thuộc
 ├─ app.NewAppFromContainer(container, cfg)
 ├─ app.Run()
 └─ chờ SIGINT/SIGTERM → container.Shutdown(ctx) → container.Close()
```

`RegisterAll` theo thứ tự phụ thuộc:

| # | Nhóm | Nội dung |
|---|---|---|
| 1 | Infrastructure | logger, DB, Redis, Kafka producer/consumer, event publisher, HTTP engine |
| 2 | Repositories | repo của các service |
| 3 | Services | business service (iam, serving, inference, usage) |
| 4 | Handlers/Routers | handler + `RegisterRoutes` |
| 5 | Workers | deployment worker, usage flush worker |

### 4.5 DI container (`pkg/di`)

API tối giản, lazy, phát hiện circular dependency — bám sát blueprint:

```go
func NewContainer() *Container
func (c *Container) Register(name string, provider ProviderFunc) error           // transient
func (c *Container) RegisterSingleton(name string, provider ProviderFunc) error  // lazy singleton
func (c *Container) Resolve(name string) (any, error)
func (c *Container) MustResolve(name string) any
func (c *Container) Shutdown(ctx context.Context) error
func (c *Container) Close() error
```

- Lỗi riêng: `ProviderError`, `CircularDependencyError`.
- Component implement interface lifecycle (`Shutdown(ctx) error` / `Close() error`) được gọi tự động.

---

## 5. Multi-binary + config bật/tắt

- Mỗi module có cờ `Services.<name>.Enabled` (mặc định true với API node).
- `cmd/server` = API node (HTTP + inference).
- `cmd/worker` = chỉ chạy deployment worker + consumer (không mở HTTP, hoặc mở health tối thiểu).
- `cmd/migrate` = chạy gormigrate `Up`; `cmd/seed` = seeder.
- `cmd/server/main.go` chỉ giữ: parse flag `--config`/`--port`, `config.Load`, `app.Run`.

---

## 6. Migration stack (chi tiết)

### 6.1 HTTP — `net/http` → Gin v1.11.0

- Handler chuyển sang `func(c *gin.Context)`; response bằng `c.JSON`.
- SSE chuyển sang `c.Stream` + `c.Writer.Flush()` (đường sống của chat — cần test riêng).
- Middleware chuyển thành `gin.HandlerFunc`; chuỗi global giữ nguyên thứ tự blueprint §5.
- Test HTTP viết lại bằng `httptest` + `gin.New()`.

### 6.2 ORM — `pgx` → GORM v1.31.0

- Định nghĩa GORM model cho: `users`, `tenants`, `tenant_memberships`, `api_keys`, `tenant_quotas`, `models`, `model_versions`, `serving_templates`, `serving_template_versions`, `deployments`, `deployment_revisions`, `endpoints`, `usage_events`, `sessions`, `messages`, `idempotency_keys`, `outbox`.
- Repository + interface mỗi aggregate; service không thấy `*gorm.DB`.
- `gormigrate` v2.1.5 thay `goose`; convert 7 migration goose hiện có (`0001`–`0007`) sang migration Go, giữ nguyên tên bảng/cột để không phá dữ liệu.
- Model dùng `gorm:"primaryKey"`, timestamp UTC.

### 6.3 Config — env → viper v1.21.0

- `configs/config.yaml` chứa mặc định; **env override** (giữ tương thích các biến `AI_FACTORY_*`).
- Flag `--config` trỏ file YAML; `--port` giữ nguyên.
- `Config` struct mở rộng nhưng giữ các field hiện có (`DatabaseURL`, `JWTSecret`, `LogLevel`, `KafkaAddr`, `RedisAddr`, `RateLimitRPM`, `RateLimitConcurrency`).

### 6.4 Logging — slog → zap + lumberjack (quyết định bridge)

- Thêm `go.uber.org/zap` + `gopkg.in/natefinch/lumberjack.v2`.
- **Quyết định**: dùng **bridge `go.uber.org/zap/exp/zapslog`** để handler zap làm backend cho `slog` — call site (`slog.Info`, 13 file non-test) **không phải sửa** trong Phase 1. Lợi ích: có zap output + rotation ngay, churn thấp.
- Nếu sau này muốn API zap native hoàn toàn → chuyển call site ở một phase riêng (không bắt buộc).
- Output giữ JSON structured (`{timestamp, level, logger, msg, ...}`), không log secret/prompt.

### 6.5 Điểm rủi ro cao nhất

Gin + GORM (Phase 3 + 2) là **thay máu**, không phải "cấu trúc lại" thuần. Làm **sau** khi Phase 1 (composition root + DI + config + logger) đã ổn định và xanh test.

---

## 7. Reliability patterns (Phase 6)

- **Transactional outbox**: bảng `outbox`; ghi event **cùng transaction** với thay đổi DB; một publisher goroutine đọc outbox → Kafka → đánh dấu đã gửi. Thay dần publish thủ công trong handler.
- **Cache-aside**: API key/model/tenant cache trên Redis; invalidation theo event.
- **Distributed lock**: `SET NX` + TTL cho thao tác one-shot (provisioning, aggregate flush).
- **Usage aggregate**: cộng dồn counter ở Redis, flush định kỳ xuống `usage_events`/aggregation.

---

## 8. Lộ trình migration (6 phase)

Mỗi phase **giữ hệ thống chạy được** và test xanh. Chỉ **Phase 1** được lập plan chi tiết ngay; các phase sau lập plan riêng sau khi phase trước xong.

| Phase | Nội dung | Verify gate |
|---|---|---|
| **1 — Nền tảng** | viper config + zap logger + `pkg/di` + `internal/app` composition root; `main.go` teo lại. Không đổi hành vi. | `go test ./...`, boot, `/health`, một chat turn |
| **2 — Data layer** | GORM models + repository/interface; convert goose → gormigrate. Handler tạm gọi repo qua interface. | test hiện có + smoke CRUD |
| **3 — HTTP layer** | `net/http` → Gin: handler, middleware, SSE `c.Stream`. | toàn bộ endpoint + SSE |
| **4 — Modularize** | Tách `controlplane` → `services/{iam,serving,usage}`; `inference` giữ chat/agent/session. | tests + smoke toàn hệ |
| **5 — Multi-binary ✅** | `cmd/{worker,migrate,seed}`; cờ `Services.*.Enabled`; worker không in-process. | deploy worker chạy độc lập |
| **6 — Reliability** | outbox, cache-aside, distributed lock, usage aggregate flush. | event không mất khi commit; cache hit; lock one-shot |

> **Ưu tiên:** Phase 1 trước (giá trị cao, rủi ro thấp, không đụng framework). Roadmap self-written inference (sampling loop Tuần 5–6) **tạm hoãn** trong lúc làm đợt này — cần xác nhận.

---

## 9. Testing strategy

- **Unit**: DI container (resolve/lazy/circular/lifecycle), config (default + env override), logger setup, repository (GORM với `sqlite` pure-Go hoặc testcontainers Postgres), state machine, auth/RBAC.
- **Integration**: Postgres (gormigrate + repo), Redis, Kafka (produce/consume), outbox → publish.
- **Contract**: route/response shape (trước/sau Gin phải giống nhau), SSE event format, event envelope §6.2.
- **E2E**: create tenant → model → deployment → READY → inference bằng API key → usage ghi nhận.

---

## 10. Non-goals / ranh giới

- **Không** Kong/proxy-server/KServe/K8s — data plane vẫn là Python worker gRPC.
- **Không** ClickHouse, Superset, analytics warehouse.
- **Không** Keycloak/OAuth/SSO (giữ JWT + API key tự viết).
- **Không** billing đầy đủ (giữ usage metering hiện có; `decimal` khi làm billing).
- **Không** i18n/currency/timezone middleware.
- **Không** đụng Python worker / tokenizer / proto / roadmap self-written inference.
- **Không** rewrite toàn bộ trong một lần — bắt buộc theo phase.

---

## 11. Rủi ro

| Rủi ro | Mức | Giảm thiểu |
|---|---|---|
| Gin+GORM thay máu toàn bộ handler/repo/test | Cao | Làm ở Phase 2–3 sau khi Phase 1 ổn; mỗi bước giữ test xanh |
| gormigrate convert phá dữ liệu đã migrate | Cao | Chạy trên DB hiện hữu; giữ nguyên tên bảng/cột; test migrate up/down |
| SSE qua Gin dễ vỡ (`c.Stream` + flush) | Cao | Test riêng cho streaming; giữ `statusRecorder.Flush` tương đương |
| Agentic loop/tool gắn chặt SSE | Trung bình | Chuyển nguyên trạng vào `services/inference`; giữ cancel propagation |
| `zapslog` bridge khác zap native | Thấp | Chấp nhận; migrate call site sau nếu cần |
| Chậm roadmap self-written inference | Trung bình | Xác nhận thứ tự ưu tiên với user |
| Blueprint chương 01–07 không có | Trung bình | Chỉ suy luận từ chương 00; ghi rõ chỗ chưa verify |

---

## 12. Câu hỏi mở

1. ~~Ưu tiên đợt kiến trúc này so với roadmap self-written inference (Tuần 5–6)?~~ **Đã chốt (D8): tạm hoãn sampling loop, ưu tiên tái kiến trúc.**
2. DB cho unit test repository: `sqlite` pure-Go (nhanh, khác dialect) hay testcontainers Postgres (chậm, đúng dialect)? — quyết trước Phase 2.
3. Có cần giữ chạy song song `net/http` cũ trong lúc migrate (strangler pattern) hay swap trực tiếp rồi sửa test? — quyết trước Phase 3.
4. ~~`cmd/worker` có mở health endpoint tối thiểu riêng không, hay chỉ log?~~ **Đã chốt (Phase 5, 2026-09-12): worker headless — chỉ log + graceful shutdown, không mở HTTP.**
