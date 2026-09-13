# Prepaid billing (wallet + ledger + per-1M-token pricing + inference gate) — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Triển khai prepaid billing theo spec `docs/superpowers/specs/2026-09-13-prepaid-billing-wallet-design.md`: bảng giá theo 1M token input/output, ví credit (wallet + ledger bất biến), nạp tiền (mock provider + idempotency), và gate inference theo cơ chế authorize → capture → release (trừ đúng usage, chặn khi hết tiền).

**Architecture:** Service mới `internal/services/billing` sở hữu pricing + ví; inference định nghĩa port `BillingGate` (consumer-defined) và adapter nối ở `internal/app` (dịch sentinel error). Reserve/settle/release trong transaction Postgres khoá `wallets FOR UPDATE`; reaper nền nhả hold hết hạn. Wiring chỉ ở composition root; `cmd/worker` không đăng ký billing.

**Tech stack:** Go (Gin + GORM + gormigrate + viper), Postgres. Không thêm dependency production mới (tiền = `int64` µcr).

---

## Decisions (locked — spec §1.4 + §12)

- **D1/D2/D5 — Prepaid, µcr `int64`, 1 reserve/HTTP request.** Tiền không dùng float; `1 credit = 10^6 µcr`.
- **D3 — Giá per model**: `price_per_million_input_tokens`, `price_per_million_output_tokens`, key `(model, currency)`.
- **D4 — Authorize→capture→release**: reserve hold trước loop; settle charge đúng usage + nhả dư ở cuối request.
- **D6 — Ledger append-only, signed, `balance_after`**; `wallets.balance` là cache của ledger.
- **D7 — Mọi mutation 1 transaction Postgres**, khoá `wallets FOR UPDATE`.
- **D8 (Q1) — Hết tiền → `402 INSUFFICIENT_CREDITS`.**
- **D9 (Q2) — Default mode `shadow`**; seed balance, bật `enforce` khi demo.
- **D10 — Thiếu pricing → fail-open** (cost 0, log + metric).
- **D11 (Q5) — Round-half-up** `(tokens×price + 500_000) / 1_000_000`.
- **D12 (Q6) — `MockProvider` auto-success**, idempotency key mọi mutation.
- **Q3 — Cho phép overdraft** (balance có thể âm khi usage vượt hold).
- **Q4 — Auto-create wallet** với `initial_allowance` (default 0).

## Global Constraints

- Không đổi JSON/SSE shape của `/v1/chat/completions` (chỉ thêm mã lỗi 402).
- Không log raw API key / prompt / secret.
- Layer rule spec §4.2: `handlers → service → repositories → models`; cross-service chỉ ở `internal/app`.
- TDD: test fail → implement → pass → `go vet ./...` → `go test ./...`.
- Commit style: `feat(billing): ...` / `feat(inference): ...` / `feat(migrations): ...`.
- Test Postgres-gated bằng `AI_FACTORY_DATABASE_URL` (mẫu `usage/aggregate_test.go`).

---

## Task B1: Schema + pricing + ví (read path) + RBAC + seed

**Files:** `internal/migrations/{0012_model_pricing.go,0013_wallet_ledger.go,migrations.go}`; `internal/services/billing/{models.go,repositories.go,repository_pricing.go,repository_wallet.go,pricing.go,wallet.go,service.go,handlers.go,router.go}` (+ tests); `internal/infrastructure/middleware/auth.go`; `internal/services/iam/rbac.go`; `internal/config/config.go`; `internal/app/{registry.go,app.go,seeder.go}`.

- [x] **Migration 0012** — `model_pricing` + unique `(model, currency)`:
  ```sql
  CREATE TABLE model_pricing (
    id UUID PRIMARY KEY,
    model TEXT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD',
    price_per_million_input_tokens  BIGINT NOT NULL DEFAULT 0,
    price_per_million_output_tokens BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model, currency)
  );
  ```
- [x] **Migration 0013** — `wallets`, `ledger_entries`, `wallet_reservations`, `topup_transactions` (spec §3.2–3.5), FK `tenant_id → tenants(id) ON DELETE CASCADE`; index `ledger_entries (tenant_id, id DESC)`, `wallet_reservations (tenant_id, status)`, `(expires_at)`.
- [x] Đăng ký `0012`, `0013` trong `migrations.go`.
- [x] **`billing/models.go`** — GORM rows mirror schema (`pricingRow`, `walletRow`, `ledgerRow`, `reservationRow`, `topupRow`) + `TableName()`.
- [x] **`billing/repositories.go`** — interface `PricingRepository` (`Upsert`, `Get(ctx, model, currency)`, `List`), `WalletRepository` (`GetOrCreate`, `Get`, `ListLedger`), `Repositories` bundle + `NewRepositories(db)`.
- [x] **`billing/pricing.go`** — domain `Price`, `UpsertPrice`, `ListPrices`, và thuần:
  ```go
  func costMicro(tokens int, pricePerMillion int64) int64 {
      if tokens <= 0 || pricePerMillion <= 0 { return 0 }
      return (int64(tokens)*pricePerMillion + 500_000) / 1_000_000
  }
  ```
  `s.pricingFor(ctx, model)` trả `ErrNoPrice` khi không có; nếu model không có giá → `cost 0` + log (D10).
- [x] **`billing/wallet.go`** — `GetWallet`, `ListLedger`; `Wallet{Balance, Reserved, Available, Currency}`.
- [x] **`billing/service.go`** — `NewService(repos, Config)`, `ErrNotFound`, `ErrInsufficientCredits`, `ErrNoPrice`.
- [x] **`billing/handlers.go` + `router.go`** — `GET /api/v1/billing/wallet`, `GET /api/v1/billing/ledger?limit=`, `GET /api/v1/billing/pricing`.
- [x] **RBAC** — thêm `billing.read`, `billing.manage` vào `middleware/auth.go` **và** `iam/rbac.go`; cập nhật `rolePermissions` (read: admin/tenant_admin/dev/viewer; manage: admin/tenant_admin).
- [x] **Config** — thêm `Services.Billing bool` + nested `Billing{Mode, Currency, InitialAllowance int64, ReservationTTL, ReaperInterval}` (env `AI_FACTORY_BILLING_*`, default mode `shadow`, currency `USD`, TTL `15m`, reaper `1m`); `configs/config.yaml`.
- [x] **Wiring B1** — `registry.go`: register `billing` + `http.billing` (API-only); `app.go` `NewAppFromContainer` thêm `"billing"`, `"http.billing"`; `NewHTTPHandler` mount `http.billing`.
- [x] **Seed** — `seeder.go` seed giá `qwen3.5-9b` (và `qwen-3b`) + ví demo tenant (env `AI_FACTORY_DEMO_CREDITS`, default ví dụ 10 credit = `10_000_000` µcr), idempotent.
- [x] **Tests** — `pricing_test.go`: round-half-up, 0 token, no-price → `cost 0`/`ErrNoPrice`. Repo Postgres-gated: upsert/list pricing; get-or-create ví; ledger list.
- [x] Verify: `go build ./...` + `go vet ./...` + `go test ./...`; `curl GET /api/v1/billing/wallet` (auth) trả balance seed.

## Task B2: Top-up + PaymentProvider + ghi pricing

**Files:** `internal/services/billing/{provider.go,wallet.go,handlers.go,router.go}` (+ tests).

- [x] **`provider.go`** — `PaymentProvider` interface (`Charge(ctx, tenantID string, amountMicro int64, idemKey string) (providerRef string, err error)`) + `MockProvider` (auto-success, `providerRef=mock:<uuid>`).
- [x] **`TopUp(ctx, tenantID, amountMicro, idemKey)`** — 1 transaction:
  - insert `topup_transactions` (unique `idempotency_key`; nếu đã tồn tại cùng amount → trả bản ghi cũ, **không** cộng lại; khác amount → `409`).
  - `provider.Charge` → success: `wallets.balance += amount`, `ledger_entries(kind='topup', amount=+, balance_after=…)`, `idempotency_key='topup:'+idemKey`.
- [x] **`Adjust`** (internal, cho admin/reconciliation): ghi ledger `kind='adjust'` ± và cập nhật balance.
- [x] **Handlers** — `POST /api/v1/billing/topup` (header `Idempotency-Key`) — body `{amount, currency?}` (amount µcr; UI đổi USD→µcr); `PUT /api/v1/billing/pricing` (upsert). Gate `billing.manage`.
- [x] **Tests** — idempotency: gọi topup 2 lần cùng key → 1 `topup_transactions`, balance cộng 1 lần; key khác amount → 409; ledger `sum(amount)==balance` sau chuỗi thao tác.

## Task B3: Inference gate — reserve / settle / release + reaper + 402

**Files:** `internal/services/billing/{wallet.go,repositories.go,repository_wallet.go,reaper.go,models.go}` (+ tests); `internal/services/inference/{handler.go,handler_test.go}`; `internal/app/{adapters.go,registry.go,app.go}`; `internal/infrastructure/observability/metrics.go`.

- [x] **`billing/wallet.go` — Reserve** (1 tx):
  ```go
  // get-or-create wallet(initial_allowance); SELECT ... FOR UPDATE
  // hold = costMicro(estInput, inPrice) + costMicro(maxOutput, outPrice)
  // if mode==enforce && available < hold -> ErrInsufficientCredits
  // UPDATE wallets SET reserved = reserved + hold
  // INSERT wallet_reservations(status='held', expires_at=now()+ttl)
  ```
  Mode `off` → trả handle rỗng (handler không gọi). Mode `shadow` → vẫn ghi hold/ledger nhưng **không** trả `ErrInsufficientCredits`.
- [x] **Settle** (1 tx, idempotent): khoá `wallet_reservations FOR UPDATE`; nếu `status='held'`: `reserved -= amount`, `balance -= charge`, ledger `kind='charge'` (`idempotency_key='charge:'+reservationID`), `status='settled'`. Nếu đã `released` (reaper) → fallback charge không hold. `charge = costMicro(prompt,in)+costMicro(completion,out)`.
- [x] **Release**: `reserved -= amount`, `status='released'`, không ledger.
- [x] **`reaper.go`** — `Reaper{Start/RunOnce/Close}` (mẫu `usage.Roller`, `roller.go`): quét `held` hết hạn, per-row trong tx nhả hold.
- [x] **`inference/handler.go`**:
  - Thêm sentinel `var ErrInsufficientCredits = errors.New("insufficient credits")` + port:
    ```go
    type BillingGate interface {
        Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error)
        Settle(ctx context.Context, reservationID string, prompt, completion int) error
        Release(ctx context.Context, reservationID string) error
    }
    ```
  - `Handler` thêm `billing BillingGate`; `NewHandler(... billing BillingGate ...)`. `billing == nil` (hoặc mode off) → bỏ qua.
  - Trong `handleOpenAIChatCompletions` sau khi dựng `params` (handler.go:163): `estInput := sum(estimateMessageTokens(m) for msgs) ; reservationID, err := h.billing.Reserve(ctx, p.TenantID, req.Model, estInput, params.MaxTokens)`; `errors.Is(err, ErrInsufficientCredits)` → 402.
  - Truyền `reservationID` vào `handleOpenAIStream`/`handleOpenAINonStream`; cộng dồn `promptTotal/completionTotal` ở mỗi `LoopEventFinal`; `defer` cuối hàm: có usage → `Settle(reservationID, promptTotal, completionTotal)`, chưa dùng → `Release(reservationID)`.
- [x] **`app/adapters.go`** — `billingGate{svc *billing.Service}` (implements `inference.BillingGate`), dịch `billing.ErrInsufficientCredits → inference.ErrInsufficientCredits`.
- [x] **Wiring B3** — `registry.go`: `http.handler` truyền `billingGate{...}`; register `billing.reaper` (API-only, khi `mode != off`). `app.go` `Run`: start/stop reaper cạnh `usage.roller`; thêm `"billing.reaper"` vào force-resolve.
- [x] **Metrics** — `billing_reservations_total`, `billing_insufficient_total`, `billing_charge_micro_total{model}` (tránh high-cardinality).
- [x] **Tests**:
  - Repo Postgres-gated: reserve giữ hold + available giảm; 2 reserve đồng thời không overdraw; settle đúng + nhả dư + idempotent; release; reaper; `sum(ledger)==balance`.
  - Handler (mock gate, không DB): `enforce` + insufficient → 402; `shadow` → chạy; `off`/nil → không gọi gate; settle 1 lần với tổng usage nhiều lượt; không usage → release.

## Task B4: Quan sát + UI + docs

**Files:** `web/src/app/(app)/platform/**`; `docs/TRACKING.md`; `CLAUDE.md`; `CONTEXT.md`.

- [ ] UI `/platform` tab **Billing**: thẻ số dư (`balance/reserved/available`), form top-up (admin), bảng ledger; bảng giá per model. **Hoãn — chưa làm; đã ghi rõ trong `docs/TRACKING.md`.**
- [x] `docs/TRACKING.md` + `CLAUDE.md` — cập nhật trạng thái prepaid billing (Key Decisions + milestone); `CONTEXT.md` — glossary `Wallet`, `Ledger`, `Reservation`, `µcr`, `Billing Gate`.

## Self-Review Checkpoints

- [x] Hết tiền (mode enforce) → `402 INSUFFICIENT_CREDITS`; mode shadow/off không chặn.
- [x] Hai request đồng thời không vượt available (khoá `wallets FOR UPDATE`).
- [x] Settle đúng `prompt×giá_in + completion×giá_out`; gọi lại không double charge.
- [x] Process chết giữa reserve và settle → reaper nhả hold; `sum(ledger_entries.amount) == wallets.balance`.
- [x] `cmd/worker` không đăng ký billing/reaper; `/v1/chat/completions` không đổi shape (chỉ thêm 402).
- [x] `go vet ./...` + `go test ./...` xanh (test DB-gated skip khi thiếu Postgres).
