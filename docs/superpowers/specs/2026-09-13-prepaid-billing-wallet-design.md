# Design — Prepaid billing: ví credit (wallet + ledger) + tính tiền theo 1M token + gate inference

- **Ngày**: 2026-09-13
- **Trạng thái**: Đã chốt D1–D12 + 6 quyết định mở (2026-09-13) — sẵn sàng lập plan triển khai.
- **Phạm vi**: Thêm **prepaid billing** vào AI Factory: ví credit multi-tenant (wallet + ledger bất biến), **bảng giá theo 1M token input/output**, nạp tiền (top-up, provider giả lập), và **gate ở đường inference** theo cơ chế **authorize → capture → release** (reserve trước, trừ theo usage thật sau). Chỉ đụng Go control plane + một nhánh trong `services/inference`; **không** đụng Python worker / proto / data plane.
- **Liên hệ spec nền**:
  - `2026-08-15-serving-platform-design.md` §9 (Billing: `pricing`, `billing_usage`, `invoices`; "gateway không biết giá") — spec này triển khai phần **prepaid** mà spec đó chưa có (spec đó chỉ có postpaid/invoice). Schema để mở đường lên postpaid.
  - `2026-08-15-inference-routing-rate-limit-design.md` (điểm gate hiện tại: `resolveForTenant`) — billing gate đặt **cạnh** rate limit, cùng chỗ.
  - `2026-09-11-modular-monolith-rearchitecture-design.md` D7 (không làm billing đầy đủ trong đợt đó) + nguyên tắc #10 (**tiền dùng decimal, không float**), layer rule §4.2, cross-service chỉ nối ở composition root.

---

## 1. Bối cảnh & mục tiêu

### 1.1 Hiện trạng đã xác minh (từ code)

- **Metering đã có, tiền chưa có**: `usage_events` (append-only, source of truth) → `usage_daily(tenant_id, model, day, prompt_tokens, completion_tokens, requests)` qua `usage.Roller` (5s, chỉ API node) — `internal/services/usage/repository_usage.go:78`. Chưa có bảng giá, ví, giao dịch.
- **`tenant_quotas(tenant_id, quota_type, limit_value, period)`** chỉ là config, **chưa enforce** (`internal/services/usage/quota.go`).
- **Gate hiện tại**: `inference.Handler.resolveForTenant` (`handler.go:74`) resolve model→deployment READY rồi áp RPM + deployment concurrency **trước** khi chạy loop. Đây là chỗ tự nhiên để chèn billing gate.
- **Usage được ghi best-effort ở cuối mỗi lượt model**: `handler.go:261` (stream) và `handler.go:322` (non-stream), qua consumer-defined port `inference.UsageRecorder` (`handler.go:41`). Một HTTP request agentic loop có thể phát **nhiều `LoopEventFinal`** (nhiều lượt model) → nhiều usage event cho 1 request.
- **`model` ghi vào usage là `Model.name`** (request `model`, ví dụ `qwen-3b`), không phải model_id → pricing phải key theo cùng tên đó.
- **Cross-service seam**: inference định nghĩa interface của riêng nó; adapter nối ở `internal/app/adapters.go` (mẫu `deploymentResolver`). Không service nào import service khác.
- **Catalog RBAC**: action const ở `internal/infrastructure/middleware/auth.go:37` và `internal/services/iam/rbac.go:11`; role map ở `rbac.go`.
- **Config**: viper, field phẳng trong `internal/config/config.go`, env `AI_FACTORY_*`; wiring ở `internal/app/registry.go`; worker nền đăng ký API-only (mẫu `usage.roller` `registry.go:163`).
- **Migration**: gormigrate Go, hiện tới `0011_usage_rollup_state` (`internal/migrations/migrations.go`).

### 1.2 Mục tiêu

1. **Tính tiền đúng theo 1M token**: mỗi model có giá **input** và **output** riêng trên 1 triệu token; chi phí = `prompt×giá_input + completion×giá_output`.
2. **Prepaid wallet**: nạp credit trước, trừ dần theo usage; ledger bất biến để audit/đối soát; số dư là nguồn sự thật.
3. **Chặn khi hết tiền** (hard-stop, cấu hình được) + **không overdraw** khi request đồng thời (reserve có khoá).
4. **Crash-safe**: hold không bị treo vĩnh viễn (reaper), settle idempotent.
5. **Mở đường postpaid**: chung bảng giá; thêm `billing_usage`/`invoices` sau mà không đổi schema ví.

### 1.3 Non-goals (YAGNI — chốt rõ)

- ❌ Cổng thanh toán thật (Stripe/VNPay…), webhook, thuế/VAT, PDF invoice, proration, đa tiền tệ/FX. Dùng `PaymentProvider` **giả lập** ở lần này.
- ❌ Postpaid/invoice theo kỳ (chỉ để schema tương thích; làm sau).
- ❌ Auto-recharge, subscription/plan, free-tier phức tạp, promo/expiry credit.
- ❌ Giá theo **region** (usage_daily không có region) → lần này key theo `model`; region để sau.
- ❌ Sửa proto / Python worker / contract inference (không đụng data plane).
- ❌ OTel/ClickHouse (giữ Prometheus + slog hiện có).

### 1.4 Quyết định đề xuất (cần chốt)

| # | Quyết định | Ghi chú |
|---|---|---|
| D1 | **Prepaid** (nạp trước, trừ dần), schema để mở lên postpaid. | "nạp tiền" của user. |
| D2 | Tiền lưu **integer micro-credit (`int64`)**, không float. 1 credit = 1 đơn vị `currency` = 10^6 µcr. | thoả D10 rearchitecture. |
| D3 | Giá per model: `price_per_million_input_tokens`, `price_per_million_output_tokens` (µcr/1M token), `currency`. | khớp tên cột spec §3.1. |
| D4 | **Authorize → capture → release**: reserve (hold) trước khi chạy loop; settle (charge đúng usage thật + nhả phần dư) ở cuối request. | chống overdraw khi concurrent. |
| D5 | **Một reserve cho mỗi HTTP request**; usage nhiều lượt model cộng dồn rồi settle 1 lần. | đơn giản hoá multi-turn loop. |
| D6 | Ledger **append-only, signed amount** + `balance_after`; balance là derived nhưng cache ở `wallets`. | audit/đối soát. |
| D7 | Reserve/settle trong **1 transaction Postgres**, khoá `wallets` `SELECT … FOR UPDATE`. | atomic, không cần Redis. |
| D8 | Hết tiền → **`402 INSUFFICIENT_CREDITS`**, tách biệt `429 RATE_LIMIT_EXCEEDED`. | ngữ nghĩa đúng; OpenAI dùng 429 `insufficient_quota` (nêu rõ alternative). |
| D9 | **Enforcement mode** `off` \| `shadow` \| `enforce`; default **`shadow`** để rollout an toàn. | prod pattern; bật `enforce` khi demo. |
| D10 | Pricing/model không có giá → **fail-open** (coi cost 0, log + metric), không chặn. | tránh chặn oan khi thiếu cấu hình. |
| D11 | Làm tròn **round-half-up** tới 1 µcr bằng số nguyên. | `(tokens×price + 500_000) / 1_000_000`. |
| D12 | Top-up qua `PaymentProvider` interface; lần này `MockProvider` (auto-success) + admin manual credit; **idempotency key** cho mọi mutation. | thay gateway thật sau. |

---

## 2. Kiến trúc

### 2.1 Luồng (một request inference)

```
Client → POST /v1/chat/completions
  │
  ▼
InferenceAuth → handleOpenAIChatCompletions
  ├─ resolveForTenant: resolve deployment + rate limit (đã có)
  ├─ billing gate: Reserve(tenant, model, estInput, maxOutput)   [MỚI]
  │     hold = estInput×giá_in + maxOutput×giá_out
  │     available = balance − reserved ; thiếu & enforce → 402
  │     → reservationID (status=held, expires_at=now+TTL)
  ├─ agentic loop (có thể nhiều LoopEventFinal)
  │     mỗi final: meter usage (đã có) + cộng dồn prompt/completion
  └─ defer: Settle(reservationID, promptTotal, completionTotal)   [MỚI]
        charge = promptTotal×giá_in + completionTotal×giá_out
        reserved −= hold ; balance −= charge ; ghi ledger (kind=charge)
        (nếu chưa dùng token nào → Release, không charge)
```

### 2.2 Vị trí enforcement

- **Điểm chặn duy nhất**: `inference.Handler.resolveForTenant` (thêm bước reserve sau rate limit). Không đụng loop/scheduler/gRPC.
- **Điểm settle**: ngay trong handler, nơi đã gọi `h.usage.RecordUsage` — dùng **cùng** `event.Usage`, cộng dồn, settle ở `defer` của request.
- Billing **không** nằm trên đường critical sau gate: settle ở cuối, best-effort log lỗi (giống metering) — nhưng **reserve là bắt buộc** (nếu reserve lỗi DB → trả 500, không cho chạy, tránh thất thoát).

### 2.3 Quan hệ prepaid → postpaid

- Dùng chung **`model_pricing`** (D3).
- Prepaid: `wallets` + `ledger_entries` + `wallet_reservations` + `topup_transactions`.
- Postpaid (sau này): thêm `billing_usage(tenant, period, input, output, amount)` + `invoices`, đọc `usage_daily` — không đụng bảng prepaid.

---

## 3. Data model

Migrations mới: **`0012_model_pricing`** và **`0013_wallet_ledger`** (gormigrate Go, thêm vào `internal/migrations/migrations.go`).

### 3.1 `model_pricing`

| Cột | Kiểu | Ghi chú |
|---|---|---|
| `model` | text | = `Model.name` (khoá cùng usage) |
| `currency` | text | ISO-4217, default `USD` |
| `price_per_million_input_tokens` | bigint | µcr / 1M token input |
| `price_per_million_output_tokens` | bigint | µcr / 1M token output |
| `created_at`, `updated_at` | timestamptz | |

- **PK/UNIQUE**: `(model, currency)`. Upsert thay giá hiện hành (lịch sử giá để sau).
- Seed demo `qwen-3b`: ví dụ input `150_000` ($0.15/1M), output `600_000` ($0.60/1M).

### 3.2 `wallets`

| Cột | Kiểu | Ghi chú |
|---|---|---|
| `tenant_id` | uuid PK | |
| `currency` | text | default `USD` |
| `balance` | bigint | µcr, số dư đã nạp (có thể <0 nếu overdraft do usage vượt hold) |
| `reserved` | bigint | µcr đang hold (active reservations) |
| `updated_at` | timestamptz | |

`available = balance − reserved`. Wallet auto-create lần reserve đầu với `billing.initial_allowance` (default 0).

### 3.3 `ledger_entries` (append-only, signed)

| Cột | Kiểu | Ghi chú |
|---|---|---|
| `id` | bigserial PK | |
| `tenant_id` | uuid | |
| `kind` | text | `topup` \| `charge` \| `grant` \| `refund` \| `adjust` |
| `amount` | bigint | **có dấu**: + cộng credit, − trừ |
| `balance_after` | bigint | số dư sau bút toán (audit) |
| `currency` | text | |
| `ref_type`, `ref_id` | text | vd `reservation`/`<id>`, `topup`/`<id>` |
| `idempotency_key` | text UNIQUE NULL | chống double-charge/credit |
| `metadata` | jsonb | |
| `created_at` | timestamptz | |

Index: `(tenant_id, id DESC)`.

### 3.4 `wallet_reservations`

| Cột | Kiểu | Ghi chú |
|---|---|---|
| `id` | uuid PK | trả về handler làm handle |
| `tenant_id` | uuid | |
| `amount` | bigint | hold |
| `currency` | text | |
| `status` | text | `held` \| `settled` \| `released` |
| `expires_at` | timestamptz | = `now()+billing.reservation_ttl` |
| `created_at`, `settled_at` | timestamptz | |

Index: `(tenant_id, status)`, `(expires_at)` cho reaper.

### 3.5 `topup_transactions`

| Cột | Kiểu | Ghi chú |
|---|---|---|
| `id` | uuid PK | |
| `tenant_id` | uuid | |
| `amount` | bigint | µcr |
| `currency` | text | |
| `provider` | text | `mock` \| `manual` (sau: `stripe`…) |
| `provider_ref` | text | |
| `status` | text | `succeeded` \| `pending` \| `failed` |
| `idempotency_key` | text UNIQUE | |
| `created_at`, `updated_at` | timestamptz | |

### 3.6 Đơn vị tiền & làm tròn

- **µcr (micro-credit)**: `1 credit = 1 currency unit = 10^6 µcr`. Không float.
- `cost_micro(tokens, price_per_million) = (tokens × price + 500_000) / 1_000_000` (round-half-up, số nguyên).
- Ví dụ: 1500 input token × `150_000` µcr/1M = 225 µcr = $0.000225.

---

## 4. Service `billing`

### 4.1 Cây thư mục + layer rule

```
internal/services/billing/
├── router.go        # RegisterRoutes(engine) — /api/v1/billing/*
├── handlers.go      # bind/validate, gọi Service
├── service.go       # NewService(repos, pricing, cfg), ErrNotFound
├── pricing.go       # UpsertPrice/ListPrices + CostMicro()
├── wallet.go        # Reserve/Settle/Release/Topup/Adjust/GetWallet/Ledger
├── provider.go      # PaymentProvider interface + MockProvider
├── reaper.go        # Reaper (release hold hết hạn)
├── models.go        # GORM rows (wallet/ledger/reservation/topup/pricing)
├── repositories.go  # interface + bundle
└── repository_*.go  # impl GORM
```

Theo layer rule rearchitecture §4.2: `handlers → service → repositories → models`; chỉ repository biết `*gorm.DB`.

### 4.2 API nội bộ (Go)

```go
// Handle inference dùng (qua port do inference định nghĩa + adapter ở app).
func (s *Service) Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (reservationID string, err error)
func (s *Service) Settle(ctx context.Context, reservationID string, prompt, completion int) error
func (s *Service) Release(ctx context.Context, reservationID string) error

// Ví / nạp.
func (s *Service) GetWallet(ctx context.Context, tenantID string) (Wallet, error)
func (s *Service) ListLedger(ctx context.Context, tenantID string, limit int) ([]LedgerEntry, error)
func (s *Service) TopUp(ctx context.Context, tenantID string, amountMicro int64, idemKey string) (TopupTransaction, error)

// Giá.
func (s *Service) UpsertPrice(ctx context.Context, p Price) (Price, error)
func (s *Service) ListPrices(ctx context.Context) ([]Price, error)
```

Error sentinel: `billing.ErrInsufficientCredits`, `billing.ErrNoPrice` (không có giá → fail-open ở handler/adapter).

### 4.3 Thuật toán reserve / settle / release

**Reserve** (1 transaction):
```sql
-- get-or-create wallet (initial_allowance)
INSERT INTO wallets(tenant_id, currency, balance, reserved)
VALUES ($t, $cur, $allowance, 0) ON CONFLICT (tenant_id) DO NOTHING;

SELECT currency, balance, reserved FROM wallets WHERE tenant_id=$t FOR UPDATE;  -- khoá

-- hold = cost(estInput) + cost(maxOutput); nếu available < hold và mode=enforce → rollback ErrInsufficientCredits
UPDATE wallets SET reserved = reserved + $hold, updated_at = now() WHERE tenant_id=$t;
INSERT INTO wallet_reservations(id, tenant_id, amount, currency, status, expires_at)
VALUES ($id, $t, $hold, $cur, 'held', now() + $ttl);
-- COMMIT
```

**Settle** (1 transaction, idempotent theo `reservationID`):
```sql
SELECT * FROM wallet_reservations WHERE id=$id FOR UPDATE;
-- nếu status != 'held': đã settle/reap → fallback charge không hold (hoặc no-op nếu settled)
charge = cost(prompt) + cost(completion);
UPDATE wallets SET reserved = reserved - amount, balance = balance - charge, updated_at=now() WHERE tenant_id=$t;
INSERT INTO ledger_entries(tenant_id, kind, amount, balance_after, currency, ref_type, ref_id, idempotency_key)
VALUES ($t, 'charge', -charge, $balanceAfter, $cur, 'reservation', $id, 'charge:'||$id);
UPDATE wallet_reservations SET status='settled', settled_at=now() WHERE id=$id;
```

**Release**: `reserved -= amount`, `status='released'`, **không** ghi ledger (chưa tiêu tiền).

**Ghi chú**:
- `charge > amount` (usage vượt ước lượng, ví dụ max_tokens bị vượt do nhiều lượt tool): balance có thể âm → chấp nhận MVP, log + metric; request sau sẽ bị chặn. (Alternative: reject khi vượt — defer.)
- `charge ≤ amount`: phần dư tự nhả vì `reserved -= amount` toàn bộ.
- Mọi mutation có `idempotency_key` (`charge:<reservationID>`) → retry không double.

### 4.4 Reaper (crash-safety)

- Hold kẹt (process chết giữa reserve và settle) → background `Reaper` quét `held` có `expires_at < now()`, per-row trong tx: `reserved -= amount`, `status='released'`.
- Chạy trên **API node** (mẫu `usage.Roller`, `registry.go:163`), interval `billing.reaper_interval` (default 1m).
- Reaper chỉ nhả hold, **không** xoá usage → nếu settle đến muộn sau khi reap, fallback charge không hold (đã ghi ở 4.3).

### 4.5 Port do inference định nghĩa + adapter ở composition root

Trong `services/inference` (không import `billing`):
```go
var ErrInsufficientCredits = errors.New("insufficient credits")

// BillingGate — interface consumer-defined; *billing.Service không thoả trực tiếp
// vì sentinel error khác package nên cần adapter dịch lỗi ở internal/app.
type BillingGate interface {
    Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error)
    Settle(ctx context.Context, reservationID string, prompt, completion int) error
    Release(ctx context.Context, reservationID string) error
}
```
Adapter `billingGate` trong `internal/app/adapters.go` map `billing.ErrInsufficientCredits → inference.ErrInsufficientCredits`; chỗ khác pass-through. Handler dùng `errors.Is(err, ErrInsufficientCredits)` → 402.

---

## 5. HTTP API + RBAC + error model

Base `/api/v1/billing`, dùng envelope hiện có (`response.WriteAPIError`).

| Route | Quyền | Mô tả |
|---|---|---|
| `GET /wallet` | `billing.read` | `{balance, reserved, available, currency}` |
| `GET /ledger?limit=` | `billing.read` | ledger gần đây (mặc định 50, ≤200) |
| `POST /topup` | `billing.manage` | body `{amount, currency}`; header `Idempotency-Key` |
| `GET /pricing` | `billing.read` | danh sách giá per model |
| `PUT /pricing` | `billing.manage` | upsert giá per `(model, currency)` |

RBAC: thêm 2 action const (`billing.read`, `billing.manage`) vào **cả** `infrastructure/middleware/auth.go` và `iam/rbac.go`, cập nhật `rolePermissions`:
- `billing.read`: PlatformAdmin, TenantAdmin, TenantDeveloper, TenantViewer.
- `billing.manage`: PlatformAdmin, TenantAdmin (nạp tiền + sửa giá).

Error model:
- Thiếu tiền (enforce): **`402`** `{"error":{"code":"INSUFFICIENT_CREDITS","message":"insufficient credits"}}`.
- Top-up: `400 INVALID_REQUEST`, `409` khi idempotency key xung đột khác amount.

---

## 6. Config

`internal/config/config.go` + `configs/config.yaml`, env `AI_FACTORY_*`:

| Field | Env | Default | Ý nghĩa |
|---|---|---|---|
| `Services.Billing` | `AI_FACTORY_SERVICES_BILLING` | `true` | bật module billing (API node) |
| `Billing.Mode` | `AI_FACTORY_BILLING_MODE` | `shadow` | `off` \| `shadow` \| `enforce` |
| `Billing.Currency` | `AI_FACTORY_BILLING_CURRENCY` | `USD` | |
| `Billing.InitialAllowance` | `AI_FACTORY_BILLING_INITIAL_ALLOWANCE` | `0` | µcr cấp khi tạo ví mới |
| `Billing.ReservationTTL` | `AI_FACTORY_BILLING_RESERVATION_TTL` | `15m` | |
| `Billing.ReaperInterval` | `AI_FACTORY_BILLING_REAPER_INTERVAL` | `1m` | |

Semantics mode:
- `off`: không reserve/settle (đường inference y như cũ).
- `shadow`: reserve/settle chạy và ghi ledger, nhưng thiếu tiền **không chặn** (log + metric). Dùng để rollout/đo.
- `enforce`: thiếu tiền → 402.

---

## 7. Wiring + multi-binary

`internal/app/registry.go`:
- Infrastructure/deps sẵn có: `db`, `cache.kv`.
- `billing` singleton: `billing.NewService(billing.NewRepositories(db.Gorm()), cfg.Billing)`.
- `billing.reaper` singleton (chỉ `cfg.Services.API`, mẫu `usage.roller`).
- `http.billing` handler khi `cfg.Services.API`.
- Truyền gate vào `http.handler` (inference): thêm tham số `billingGate{svc: cc.MustResolve("billing").(*billing.Service)}` (adapter).
- `App.Run`: start/stop `billing.reaper` giống `usage.roller` (chỉ khi `Billing.Mode != off`).
- Worker node (`cmd/worker`) **không** đăng ký billing (API-only), không ảnh hưởng multi-binary.

---

## 8. Observability

- Metrics (Prometheus, tránh high-cardinality): `billing_reservations_total`, `billing_insufficient_total`, `billing_charge_micro_total{model}`, `billing_topup_micro_total`, `billing_ledger_mismatch_total` (đối soát).
- Log `slog`: reserve/settle/reap/topup với `tenant_id`, `reservation_id`, `model` — **không** log key/prompt.
- Prometheus balance gauge theo tenant là high-cardinality → bỏ qua (hoặc tách sau).

---

## 9. Testing

1. **Unit — pricing/cost** (`pricing_test.go`): round-half-up, biên 0 token, model không có giá → `ErrNoPrice`, giá output khác input.
2. **Repository — Postgres-gated** (mẫu `usage/aggregate_test.go`, gate `AI_FACTORY_DATABASE_URL`):
   - Reserve giữ hold, `available` giảm; hai reserve đồng thời không overdraw (khoá).
   - Settle charge đúng + nhả phần dư; idempotent (gọi 2 lần không double).
   - Release nhả hold, không ledger.
   - Reaper nhả hold hết hạn.
   - `sum(ledger.amount) == balance` sau chuỗi thao tác.
   - TopUp idempotency: cùng key → 1 transaction, không double credit.
3. **Handler — inference** (`handler_test.go`, gate mock, không DB):
   - `enforce` + gate trả `ErrInsufficientCredits` → 402.
   - `shadow` + thiếu tiền → request chạy bình thường.
   - `off` → gate không được gọi.
   - Settle được gọi 1 lần với tổng usage nhiều lượt; lỗi khi chưa dùng → Release.
4. **RBAC**: `RoleAllows` cho `billing.read/manage` theo role.
5. `go vet ./...` + `go test ./...` xanh (test DB-gated skip khi không có Postgres).

---

## 10. UI (tối thiểu)

- `/platform` thêm tab **Billing**: thẻ số dư (`balance/reserved/available`), form top-up (admin, nhập USD → gửi µcr), bảng ledger gần đây.
- `/infra` (hoặc tab Billing): bảng giá per model (admin sửa).
- `/chat`: hiển thị số dư + cảnh báo khi thấp gần ngưỡng (không bắt buộc trong đợt đầu).
- UI deferred (giữ pattern hiện có): auto-refresh số dư sau mỗi lượt, biểu đồ chi phí theo ngày.

---

## 11. Lộ trình triển khai (milestone)

| # | Nội dung | Demo |
|---|---|---|
| **B1** Schema + service + pricing | Migration `0012`+`0013`; `billing.Service`; `GET /wallet`, `/ledger`, `/pricing`; RBAC; seed giá `qwen-3b` + balance demo | Xem ví demo + giá model |
| **B2** Top-up | `PaymentProvider`/`MockProvider`; `POST /topup` (idempotency); `PUT /pricing`; UI Billing tab | Nạp $10 → ledger `topup` |
| **B3** Inference gate | `BillingGate` port + adapter; reserve/settle/release trong handler; mode off/shadow/enforce; 402 | Gọi chat → trừ tiền; hết tiền → 402 |
| **B4** Hardening | `Reaper`; reconciliation `sum(ledger)==balance`; metrics; concurrency test | Crash giữa chừng → hold được nhả |

---

## 12. Scope cut / quyết định đã chốt

**Scope cut**: xem §1.3.

**6 quyết định mở — ĐÃ CHỐT (2026-09-13)**:

| # | Chốt |
|---|---|
| Q1 (D8) | Mã lỗi **`402 INSUFFICIENT_CREDITS`** (tách khỏi 429 rate limit). |
| Q2 (D9) | Default mode = **`shadow`**; seed sẵn balance, bật `enforce` khi demo. |
| Q3 | **Cho phép overdraft** (balance có thể âm khi usage vượt hold); request sau bị chặn. |
| Q4 | **Auto-create wallet** với `initial_allowance` (default 0) ở lần reserve đầu. |
| Q5 | Đơn vị tiền = **µcr `int64`** (không dùng `shopspring/decimal`). |
| Q6 | **`MockProvider` auto-success** — chưa cần luồng `pending`/admin confirm. |

---

## 13. Files touched (dự kiến)

- `docs/superpowers/specs/2026-09-13-prepaid-billing-wallet-design.md` — spec này.
- `go-server/internal/migrations/0012_model_pricing.go`, `0013_wallet_ledger.go`, `migrations.go`.
- `go-server/internal/services/billing/**` (service, repo, provider, reaper, handlers, router, tests).
- `go-server/internal/services/inference/handler.go` — thêm `BillingGate` + reserve/settle/release (và `handler_test.go`).
- `go-server/internal/app/adapters.go` — adapter `billingGate` (dịch sentinel error).
- `go-server/internal/app/registry.go`, `internal/app/app.go` (start/stop reaper).
- `go-server/internal/config/config.go` + `config/config_test.go` + `configs/config.yaml`.
- `go-server/internal/infrastructure/middleware/auth.go` + `internal/services/iam/rbac.go` — action `billing.read/manage`.
- `go-server/internal/app/seeder.go` — seed giá + ví demo.
- `go-server/internal/infrastructure/observability/metrics.go` — metric billing.
- `web/src/app/(app)/platform/**` — tab Billing (nếu làm UI).
- `CLAUDE.md`, `docs/TRACKING.md`, `CONTEXT.md` — cập nhật trạng thái + glossary khi triển khai.
