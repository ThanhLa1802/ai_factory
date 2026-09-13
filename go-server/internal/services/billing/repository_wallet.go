package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type walletRepo struct{ db *gorm.DB }

func (r *walletRepo) GetOrCreate(ctx context.Context, tenantID, currency string, initialAllowance int64) (Wallet, error) {
	row := walletRow{TenantID: tenantID, Currency: currency, Balance: initialAllowance}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error; err != nil {
		return Wallet{}, fmt.Errorf("create wallet: %w", err)
	}
	return r.Get(ctx, tenantID)
}

func (r *walletRepo) Get(ctx context.Context, tenantID string) (Wallet, error) {
	var row walletRow
	err := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Wallet{TenantID: tenantID, Currency: "", Balance: 0, Reserved: 0, Available: 0}, nil
	}
	if err != nil {
		return Wallet{}, fmt.Errorf("get wallet: %w", err)
	}
	return toWallet(row), nil
}

func (r *walletRepo) ListLedger(ctx context.Context, tenantID string, limit int) ([]LedgerEntry, error) {
	var rows []ledgerRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("id DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	out := make([]LedgerEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, toLedgerEntry(row))
	}
	return out, nil
}

// Reserve get-or-creates the wallet, locks it, checks available budget (when
// enforcing) and places a hold in one transaction.
func (r *walletRepo) Reserve(ctx context.Context, in ReserveInput) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		w := walletRow{TenantID: in.TenantID, Currency: in.Currency, Balance: in.InitialAllowance}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&w).Error; err != nil {
			return fmt.Errorf("reserve: create wallet: %w", err)
		}
		var cur walletRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ?", in.TenantID).First(&cur).Error; err != nil {
			return fmt.Errorf("reserve: lock wallet: %w", err)
		}
		if in.Enforce && in.Amount > 0 && cur.Balance-cur.Reserved < in.Amount {
			return ErrInsufficientCredits
		}
		if err := tx.Model(&walletRow{}).Where("tenant_id = ?", in.TenantID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("reserved + ?", in.Amount),
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("reserve: hold: %w", err)
		}
		res := reservationRow{
			ID: in.ReservationID, TenantID: in.TenantID, Amount: in.Amount,
			InputPrice: in.InputPrice, OutputPrice: in.OutputPrice,
			Currency: in.Currency, Status: reservationHeld,
			ExpiresAt: time.Now().UTC().Add(in.TTL),
		}
		if err := tx.Create(&res).Error; err != nil {
			return fmt.Errorf("reserve: create reservation: %w", err)
		}
		return nil
	})
}

// Settle captures the real usage against the hold, releases the remainder and
// appends a charge ledger entry. Idempotent: an already-settled reservation is a
// no-op; a reaped (released) reservation is still charged without a hold.
func (r *walletRepo) Settle(ctx context.Context, reservationID string, promptTokens, completionTokens int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var res reservationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", reservationID).First(&res).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("settle: lock reservation: %w", err)
		}
		if res.Status == reservationSettled {
			return nil
		}
		charge := costMicro(promptTokens, res.InputPrice) + costMicro(completionTokens, res.OutputPrice)

		var w walletRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ?", res.TenantID).First(&w).Error; err != nil {
			return fmt.Errorf("settle: lock wallet: %w", err)
		}
		newBalance := w.Balance - charge
		newReserved := w.Reserved
		if res.Status == reservationHeld {
			newReserved -= res.Amount
		}
		if err := tx.Model(&walletRow{}).Where("tenant_id = ?", res.TenantID).
			Updates(map[string]any{
				"balance":    newBalance,
				"reserved":   newReserved,
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("settle: update wallet: %w", err)
		}

		key := "charge:" + res.ID
		entry := ledgerRow{
			TenantID: res.TenantID, Kind: LedgerCharge, Amount: -charge,
			BalanceAfter: newBalance, Currency: res.Currency,
			RefType: "reservation", RefID: res.ID, IdempotencyKey: &key,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return fmt.Errorf("settle: ledger: %w", err)
		}
		observability.RecordBillingChargeMicro(charge)
		now := time.Now().UTC()
		return tx.Model(&reservationRow{}).Where("id = ?", res.ID).
			Updates(map[string]any{"status": reservationSettled, "settled_at": now}).Error
	})
}

// Release frees an unused hold without charging.
func (r *walletRepo) Release(ctx context.Context, reservationID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var res reservationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", reservationID).First(&res).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("release: lock reservation: %w", err)
		}
		if res.Status != reservationHeld {
			return nil
		}
		if err := tx.Model(&walletRow{}).Where("tenant_id = ?", res.TenantID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("reserved - ?", res.Amount),
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("release: update wallet: %w", err)
		}
		return tx.Model(&reservationRow{}).Where("id = ?", res.ID).
			Update("status", reservationReleased).Error
	})
}

// ReleaseExpired reaps holds whose lease lapsed (a process died between reserve
// and settle), returning how many were released.
func (r *walletRepo) ReleaseExpired(ctx context.Context, limit int) (int, error) {
	n := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []reservationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND expires_at < now()", reservationHeld).
			Limit(limit).Find(&rows).Error; err != nil {
			return fmt.Errorf("reaper: scan: %w", err)
		}
		for _, res := range rows {
			if err := tx.Model(&walletRow{}).Where("tenant_id = ?", res.TenantID).
				Updates(map[string]any{
					"reserved":   gorm.Expr("reserved - ?", res.Amount),
					"updated_at": time.Now().UTC(),
				}).Error; err != nil {
				return fmt.Errorf("reaper: update wallet: %w", err)
			}
			if err := tx.Model(&reservationRow{}).Where("id = ?", res.ID).
				Update("status", reservationReleased).Error; err != nil {
				return fmt.Errorf("reaper: release reservation: %w", err)
			}
			n++
		}
		return nil
	})
	return n, err
}

type topupRepo struct{ db *gorm.DB }

// TopUp records one idempotent top-up and credits the wallet in a single
// transaction. A repeated idempotency key with the same amount returns the
// existing transaction without crediting again.
func (r *topupRepo) TopUp(ctx context.Context, in TopupInput) (TopupTransaction, error) {
	var out TopupTransaction
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing topupRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("idempotency_key = ?", in.IdemKey).First(&existing).Error
		if err == nil {
			if existing.TenantID != in.TenantID || existing.Amount != in.Amount {
				return ErrIdempotencyConflict
			}
			out = toTopup(existing)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("topup: lookup: %w", err)
		}

		row := topupRow{
			ID: uuid.NewString(), TenantID: in.TenantID, Amount: in.Amount,
			Currency: in.Currency, Provider: in.Provider, ProviderRef: in.ProviderRef,
			Status: "succeeded", IdempotencyKey: in.IdemKey,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("topup: insert: %w", err)
		}

		w := walletRow{TenantID: in.TenantID, Currency: in.Currency}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&w).Error; err != nil {
			return fmt.Errorf("topup: create wallet: %w", err)
		}
		var cur walletRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ?", in.TenantID).First(&cur).Error; err != nil {
			return fmt.Errorf("topup: lock wallet: %w", err)
		}
		newBalance := cur.Balance + in.Amount
		if err := tx.Model(&walletRow{}).Where("tenant_id = ?", in.TenantID).
			Updates(map[string]any{"balance": newBalance, "updated_at": time.Now().UTC()}).Error; err != nil {
			return fmt.Errorf("topup: credit: %w", err)
		}
		key := "topup:" + in.IdemKey
		entry := ledgerRow{
			TenantID: in.TenantID, Kind: LedgerTopup, Amount: in.Amount,
			BalanceAfter: newBalance, Currency: in.Currency,
			RefType: "topup", RefID: row.ID, IdempotencyKey: &key,
		}
		if err := tx.Create(&entry).Error; err != nil {
			return fmt.Errorf("topup: ledger: %w", err)
		}
		out = toTopup(row)
		return nil
	})
	return out, err
}
