package billing

import (
	"context"
	"errors"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/google/uuid"
)

// Wallet is a tenant's credit balance. Available = Balance - Reserved.
type Wallet struct {
	TenantID  string `json:"tenant_id"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
	Reserved  int64  `json:"reserved"`
	Available int64  `json:"available"`
}

// LedgerEntry is one immutable, signed movement of credits.
type LedgerEntry struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	Amount       int64     `json:"amount"`
	BalanceAfter int64     `json:"balance_after"`
	Currency     string    `json:"currency"`
	RefType      string    `json:"ref_type"`
	RefID        string    `json:"ref_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) GetWallet(ctx context.Context, tenantID string) (Wallet, error) {
	return s.repos.Wallets.Get(ctx, tenantID)
}

func (s *Service) ListLedger(ctx context.Context, tenantID string, limit int) ([]LedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	return s.repos.Wallets.ListLedger(ctx, tenantID, limit)
}

// Reserve places an upper-bound hold for a request. It returns a reservation id
// used later to settle. In enforce mode an unaffordable hold returns
// ErrInsufficientCredits; in shadow mode the hold is placed regardless. A model
// with no price fails open (zero hold) per design D10.
func (s *Service) Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error) {
	if s.cfg.Mode == ModeOff {
		return "", nil
	}
	price, err := s.priceFor(ctx, model)
	if err != nil {
		if !errors.Is(err, ErrNoPrice) {
			return "", err
		}
		s.cfg.Log.Warn("billing: no price for model; charging zero", "model", model)
		price = Price{Currency: s.cfg.Currency}
	}
	hold := costMicro(estInput, price.PricePerMillionInputTokens) +
		costMicro(maxOutput, price.PricePerMillionOutputTokens)
	currency := price.Currency
	if currency == "" {
		currency = s.cfg.Currency
	}
	id := uuid.NewString()
	err = s.repos.Wallets.Reserve(ctx, ReserveInput{
		TenantID:         tenantID,
		Currency:         currency,
		InitialAllowance: s.cfg.InitialAllowance,
		Amount:           hold,
		InputPrice:       price.PricePerMillionInputTokens,
		OutputPrice:      price.PricePerMillionOutputTokens,
		ReservationID:    id,
		TTL:              s.cfg.ReservationTTL,
		Enforce:          s.cfg.Mode == ModeEnforce,
	})
	if err != nil {
		if errors.Is(err, ErrInsufficientCredits) {
			observability.IncBillingInsufficient(tenantID)
		}
		return "", err
	}
	observability.RecordBillingReservation(model)
	return id, nil
}

// Settle captures the real usage against the hold and releases the remainder.
func (s *Service) Settle(ctx context.Context, reservationID string, promptTokens, completionTokens int) error {
	if reservationID == "" || s.cfg.Mode == ModeOff {
		return nil
	}
	return s.repos.Wallets.Settle(ctx, reservationID, promptTokens, completionTokens)
}

// Release frees an unused hold without charging.
func (s *Service) Release(ctx context.Context, reservationID string) error {
	if reservationID == "" || s.cfg.Mode == ModeOff {
		return nil
	}
	return s.repos.Wallets.Release(ctx, reservationID)
}

// ReapExpired releases expired holds (crash safety).
func (s *Service) ReapExpired(ctx context.Context, limit int) (int, error) {
	if s.cfg.Mode == ModeOff {
		return 0, nil
	}
	return s.repos.Wallets.ReleaseExpired(ctx, limit)
}

// Mode reports the configured enforcement mode.
func (s *Service) Mode() string { return s.cfg.Mode }
