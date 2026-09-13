package billing

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Repository interfaces. The service layer depends on these; only the
// implementations in this package know GORM.

// PricingRepository stores per-model prices (µcr per 1M tokens).
type PricingRepository interface {
	Upsert(ctx context.Context, p *Price) error
	Get(ctx context.Context, model, currency string) (*Price, error)
	List(ctx context.Context) ([]Price, error)
}

// ReserveInput is the persistent form of one hold: the pre-computed amount plus
// the unit-price snapshot used later to settle.
type ReserveInput struct {
	TenantID         string
	Currency         string
	InitialAllowance int64
	Amount           int64
	InputPrice       int64
	OutputPrice      int64
	ReservationID    string
	TTL              time.Duration
	Enforce          bool
}

// WalletRepository owns the wallet/ledger/reservation aggregate. Reserve,
// Settle and Release each run in one transaction and lock the wallet row so
// concurrent requests cannot overdraw.
type WalletRepository interface {
	GetOrCreate(ctx context.Context, tenantID, currency string, initialAllowance int64) (Wallet, error)
	Get(ctx context.Context, tenantID string) (Wallet, error)
	ListLedger(ctx context.Context, tenantID string, limit int) ([]LedgerEntry, error)
	Reserve(ctx context.Context, in ReserveInput) error
	Settle(ctx context.Context, reservationID string, promptTokens, completionTokens int) error
	Release(ctx context.Context, reservationID string) error
	ReleaseExpired(ctx context.Context, limit int) (int, error)
}

// TopupInput is one idempotent top-up (provider already charged).
type TopupInput struct {
	TenantID    string
	Currency    string
	Amount      int64
	Provider    string
	ProviderRef string
	IdemKey     string
}

// TopupRepository records top-up transactions and credits the wallet.
type TopupRepository interface {
	TopUp(ctx context.Context, in TopupInput) (TopupTransaction, error)
}

// Repositories bundles every repository the billing service needs.
type Repositories struct {
	Pricing PricingRepository
	Wallets WalletRepository
	Topups  TopupRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{
		Pricing: &pricingRepo{db: db},
		Wallets: &walletRepo{db: db},
		Topups:  &topupRepo{db: db},
	}
}
