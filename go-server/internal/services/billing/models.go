package billing

import "time"

// GORM row types. These mirror the SQL schema exactly (table + column names)
// and are deliberately separate from the domain/JSON structs; repositories map
// between them. Money is stored as integer micro-credits (µcr); 1 credit unit
// equals 1,000,000 µcr.

type pricingRow struct {
	ID                          string    `gorm:"column:id;type:uuid;primaryKey"`
	Model                       string    `gorm:"column:model"`
	Currency                    string    `gorm:"column:currency"`
	PricePerMillionInputTokens  int64     `gorm:"column:price_per_million_input_tokens"`
	PricePerMillionOutputTokens int64     `gorm:"column:price_per_million_output_tokens"`
	CreatedAt                   time.Time `gorm:"column:created_at"`
	UpdatedAt                   time.Time `gorm:"column:updated_at"`
}

func (pricingRow) TableName() string { return "model_pricing" }

type walletRow struct {
	TenantID  string    `gorm:"column:tenant_id;type:uuid;primaryKey"`
	Currency  string    `gorm:"column:currency"`
	Balance   int64     `gorm:"column:balance"`
	Reserved  int64     `gorm:"column:reserved"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (walletRow) TableName() string { return "wallets" }

type ledgerRow struct {
	ID             int64     `gorm:"column:id;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;type:uuid"`
	Kind           string    `gorm:"column:kind"`
	Amount         int64     `gorm:"column:amount"`
	BalanceAfter   int64     `gorm:"column:balance_after"`
	Currency       string    `gorm:"column:currency"`
	RefType        string    `gorm:"column:ref_type"`
	RefID          string    `gorm:"column:ref_id"`
	IdempotencyKey *string   `gorm:"column:idempotency_key"`
	Metadata       []byte    `gorm:"column:metadata;type:jsonb"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (ledgerRow) TableName() string { return "ledger_entries" }

type reservationRow struct {
	ID          string     `gorm:"column:id;type:uuid;primaryKey"`
	TenantID    string     `gorm:"column:tenant_id;type:uuid"`
	Amount      int64      `gorm:"column:amount"`
	InputPrice  int64      `gorm:"column:input_price"`
	OutputPrice int64      `gorm:"column:output_price"`
	Currency    string     `gorm:"column:currency"`
	Status      string     `gorm:"column:status"`
	ExpiresAt   time.Time  `gorm:"column:expires_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	SettledAt   *time.Time `gorm:"column:settled_at"`
}

func (reservationRow) TableName() string { return "wallet_reservations" }

type topupRow struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;type:uuid"`
	Amount         int64     `gorm:"column:amount"`
	Currency       string    `gorm:"column:currency"`
	Provider       string    `gorm:"column:provider"`
	ProviderRef    string    `gorm:"column:provider_ref"`
	Status         string    `gorm:"column:status"`
	IdempotencyKey string    `gorm:"column:idempotency_key"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (topupRow) TableName() string { return "topup_transactions" }

// Reservation lifecycle statuses.
const (
	reservationHeld     = "held"
	reservationSettled  = "settled"
	reservationReleased = "released"
)

// Ledger entry kinds.
const (
	LedgerTopup  = "topup"
	LedgerCharge = "charge"
	LedgerGrant  = "grant"
	LedgerRefund = "refund"
	LedgerAdjust = "adjust"
)

// --- mappers (row → domain) ---

func toPrice(r pricingRow) Price {
	return Price{
		Model:                       r.Model,
		Currency:                    r.Currency,
		PricePerMillionInputTokens:  r.PricePerMillionInputTokens,
		PricePerMillionOutputTokens: r.PricePerMillionOutputTokens,
	}
}

func toWallet(r walletRow) Wallet {
	return Wallet{
		TenantID:  r.TenantID,
		Currency:  r.Currency,
		Balance:   r.Balance,
		Reserved:  r.Reserved,
		Available: r.Balance - r.Reserved,
	}
}

func toLedgerEntry(r ledgerRow) LedgerEntry {
	return LedgerEntry{
		ID:           r.ID,
		Kind:         r.Kind,
		Amount:       r.Amount,
		BalanceAfter: r.BalanceAfter,
		Currency:     r.Currency,
		RefType:      r.RefType,
		RefID:        r.RefID,
		CreatedAt:    r.CreatedAt,
	}
}
