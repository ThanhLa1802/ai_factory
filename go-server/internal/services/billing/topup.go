package billing

import (
	"context"
	"time"
)

// TopupTransaction records one top-up. Amount is in micro-credits.
type TopupTransaction struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func toTopup(r topupRow) TopupTransaction {
	return TopupTransaction{
		ID: r.ID, TenantID: r.TenantID, Amount: r.Amount, Currency: r.Currency,
		Provider: r.Provider, ProviderRef: r.ProviderRef, Status: r.Status, CreatedAt: r.CreatedAt,
	}
}

// TopUp charges the provider then credits the wallet, idempotent per idemKey.
func (s *Service) TopUp(ctx context.Context, tenantID string, amountMicro int64, idemKey string) (TopupTransaction, error) {
	if amountMicro <= 0 {
		return TopupTransaction{}, ErrInvalidAmount
	}
	if idemKey == "" {
		return TopupTransaction{}, ErrInvalidRequest
	}
	ref, err := s.provider.Charge(ctx, tenantID, amountMicro, idemKey)
	if err != nil {
		return TopupTransaction{}, err
	}
	return s.repos.Topups.TopUp(ctx, TopupInput{
		TenantID:    tenantID,
		Currency:    s.cfg.Currency,
		Amount:      amountMicro,
		Provider:    s.provider.Name(),
		ProviderRef: ref,
		IdemKey:     idemKey,
	})
}
