package billing

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newTestService(t *testing.T, mode string) (*Service, *gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewService(NewRepositories(d.Gorm()), Config{
		Mode: mode, Currency: "USD", ReservationTTL: time.Minute,
	}, MockProvider{})
	t.Cleanup(func() { _ = d.Close() })
	return svc, d.Gorm(), func() {}
}

func seedTenant(t *testing.T, g *gorm.DB) string {
	t.Helper()
	id := uuid.NewString()
	if err := g.Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, id, "bill-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _ = g.Exec(`DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func ledgerSum(t *testing.T, g *gorm.DB, tenantID string) int64 {
	t.Helper()
	var sum int64
	if err := g.Raw(`SELECT COALESCE(SUM(amount),0) FROM ledger_entries WHERE tenant_id = ?`, tenantID).Scan(&sum).Error; err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	return sum
}

func TestPricingUpsertGetList(t *testing.T) {
	svc, _, cleanup := newTestService(t, ModeShadow)
	defer cleanup()
	ctx := context.Background()

	if _, err := svc.UpsertPrice(ctx, Price{Model: "m1", PricePerMillionInputTokens: 150_000, PricePerMillionOutputTokens: 600_000}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Upsert replaces rather than duplicating.
	if _, err := svc.UpsertPrice(ctx, Price{Model: "m1", PricePerMillionInputTokens: 200_000, PricePerMillionOutputTokens: 700_000}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if _, err := svc.priceFor(ctx, "does-not-exist"); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("priceFor unknown = %v, want ErrNoPrice", err)
	}
	got, err := svc.priceFor(ctx, "m1")
	if err != nil {
		t.Fatalf("priceFor: %v", err)
	}
	if got.PricePerMillionInputTokens != 200_000 || got.Currency != "USD" {
		t.Fatalf("price = %+v, want input=200000 currency=USD", got)
	}
	if _, err := svc.UpsertPrice(ctx, Price{Model: "  "}); !errors.Is(err, ErrInvalidPrice) {
		t.Fatalf("blank model = %v, want ErrInvalidPrice", err)
	}
}

func TestTopUpIdempotent(t *testing.T) {
	svc, g, cleanup := newTestService(t, ModeShadow)
	defer cleanup()
	ctx := context.Background()
	tenantID := seedTenant(t, g)

	first, err := svc.TopUp(ctx, tenantID, 5_000_000, "key-1:"+tenantID)
	if err != nil {
		t.Fatalf("topup 1: %v", err)
	}
	second, err := svc.TopUp(ctx, tenantID, 5_000_000, "key-1:"+tenantID)
	if err != nil {
		t.Fatalf("topup 2: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent topup returned different rows: %s vs %s", first.ID, second.ID)
	}
	w, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if w.Balance != 5_000_000 {
		t.Fatalf("balance = %d, want 5000000 (credited once)", w.Balance)
	}
	if _, err := svc.TopUp(ctx, tenantID, 9_000_000, "key-1:"+tenantID); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting key = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := svc.TopUp(ctx, tenantID, 1_000_000, "key-2:"+tenantID); err != nil {
		t.Fatalf("topup 3: %v", err)
	}
	if w, _ = svc.GetWallet(ctx, tenantID); w.Balance != 6_000_000 {
		t.Fatalf("balance = %d, want 6000000", w.Balance)
	}
	if sum := ledgerSum(t, g, tenantID); sum != w.Balance {
		t.Fatalf("ledger sum %d != balance %d", sum, w.Balance)
	}
}

func TestReserveSettleRelease(t *testing.T) {
	svc, g, cleanup := newTestService(t, ModeEnforce)
	defer cleanup()
	ctx := context.Background()
	tenantID := seedTenant(t, g)

	if _, err := svc.UpsertPrice(ctx, Price{Model: "m1", PricePerMillionInputTokens: 150_000, PricePerMillionOutputTokens: 600_000}); err != nil {
		t.Fatalf("price: %v", err)
	}
	if _, err := svc.TopUp(ctx, tenantID, 10_000_000, "fund:"+tenantID); err != nil {
		t.Fatalf("fund: %v", err)
	}

	// hold = 1000×150000/1e6 + 1000×600000/1e6 = 150 + 600 = 750
	id, err := svc.Reserve(ctx, tenantID, "m1", 1000, 1000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	w, _ := svc.GetWallet(ctx, tenantID)
	if w.Reserved != 750 || w.Available != 10_000_000-750 {
		t.Fatalf("after reserve reserved=%d available=%d, want 750/%d", w.Reserved, w.Available, 10_000_000-750)
	}

	// charge = 1000×150000/1e6 + 500×600000/1e6 = 150 + 300 = 450
	if err := svc.Settle(ctx, id, 1000, 500); err != nil {
		t.Fatalf("settle: %v", err)
	}
	w, _ = svc.GetWallet(ctx, tenantID)
	if w.Balance != 10_000_000-450 || w.Reserved != 0 {
		t.Fatalf("after settle balance=%d reserved=%d, want %d/0", w.Balance, w.Reserved, 10_000_000-450)
	}
	// Idempotent: settling again does not charge twice.
	if err := svc.Settle(ctx, id, 1000, 500); err != nil {
		t.Fatalf("settle 2: %v", err)
	}
	if w, _ = svc.GetWallet(ctx, tenantID); w.Balance != 10_000_000-450 {
		t.Fatalf("balance after re-settle = %d, want %d", w.Balance, 10_000_000-450)
	}

	// Release frees a hold without charging.
	id2, err := svc.Reserve(ctx, tenantID, "m1", 1000, 0)
	if err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	if err := svc.Release(ctx, id2); err != nil {
		t.Fatalf("release: %v", err)
	}
	if w, _ = svc.GetWallet(ctx, tenantID); w.Reserved != 0 || w.Balance != 10_000_000-450 {
		t.Fatalf("after release balance=%d reserved=%d, want %d/0", w.Balance, w.Reserved, 10_000_000-450)
	}
	if sum := ledgerSum(t, g, tenantID); sum != w.Balance {
		t.Fatalf("ledger sum %d != balance %d", sum, w.Balance)
	}
}

func TestReserveInsufficientAndShadow(t *testing.T) {
	svc, g, cleanup := newTestService(t, ModeEnforce)
	defer cleanup()
	ctx := context.Background()

	if _, err := svc.UpsertPrice(ctx, Price{Model: "m1", PricePerMillionOutputTokens: 600_000}); err != nil {
		t.Fatalf("price: %v", err)
	}
	// Enforce + no funds: rejected.
	enforceTenant := seedTenant(t, g)
	if _, err := svc.Reserve(ctx, enforceTenant, "m1", 0, 1000); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("reserve unfunded = %v, want ErrInsufficientCredits", err)
	}

	// Shadow mode places the hold regardless.
	shadowTenant := seedTenant(t, g)
	shadow := NewService(NewRepositories(g), Config{Mode: ModeShadow, Currency: "USD", ReservationTTL: time.Minute}, MockProvider{})
	if _, err := shadow.Reserve(ctx, shadowTenant, "m1", 0, 1000); err != nil {
		t.Fatalf("shadow reserve = %v, want nil", err)
	}

	// Unknown model fails open (zero hold) even in enforce mode.
	unpricedTenant := seedTenant(t, g)
	if _, err := svc.Reserve(ctx, unpricedTenant, "no-price-model", 1000, 1000); err != nil {
		t.Fatalf("reserve unpriced = %v, want nil (fail-open)", err)
	}
}

func TestReapExpired(t *testing.T) {
	svc, g, cleanup := newTestService(t, ModeEnforce)
	defer cleanup()
	ctx := context.Background()
	tenantID := seedTenant(t, g)

	if _, err := svc.UpsertPrice(ctx, Price{Model: "m1", PricePerMillionOutputTokens: 600_000}); err != nil {
		t.Fatalf("price: %v", err)
	}
	if _, err := svc.TopUp(ctx, tenantID, 10_000_000, "fund:"+tenantID); err != nil {
		t.Fatalf("fund: %v", err)
	}
	id, err := svc.Reserve(ctx, tenantID, "m1", 0, 1000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := g.Exec(`UPDATE wallet_reservations SET expires_at = now() - interval '1 minute' WHERE id = ?`, id).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}
	n, err := svc.ReapExpired(ctx, 10)
	if err != nil || n < 1 {
		t.Fatalf("reap = (%d,%v), want >=1,nil", n, err)
	}
	if w, _ := svc.GetWallet(ctx, tenantID); w.Reserved != 0 {
		t.Fatalf("reserved after reap = %d, want 0", w.Reserved)
	}
}

// TestRowsSelectable is a schema-parity guard: each GORM row type must be
// selectable against the migrated tables.
func TestRowsSelectable(t *testing.T) {
	_, g, cleanup := newTestService(t, ModeShadow)
	defer cleanup()
	for _, c := range []struct {
		name string
		dest any
	}{
		{"model_pricing", &[]pricingRow{}},
		{"wallets", &[]walletRow{}},
		{"ledger_entries", &[]ledgerRow{}},
		{"wallet_reservations", &[]reservationRow{}},
		{"topup_transactions", &[]topupRow{}},
	} {
		if err := g.Limit(1).Find(c.dest).Error; err != nil {
			t.Errorf("%s: select failed (column drift?): %v", c.name, err)
		}
	}
}
