package billing

import (
	"context"

	"github.com/google/uuid"
)

// PaymentProvider charges money for a top-up. The real gateway is swapped in
// later behind this seam; the mock auto-succeeds and is idempotent per key.
type PaymentProvider interface {
	// Name identifies the provider on the topup_transactions row.
	Name() string
	// Charge returns a provider reference. Implementations must be idempotent
	// for a given idempotency key.
	Charge(ctx context.Context, tenantID string, amountMicro int64, idempotencyKey string) (string, error)
}

// MockProvider is the default dev provider: it always succeeds.
type MockProvider struct{}

func (MockProvider) Name() string { return "mock" }

func (MockProvider) Charge(_ context.Context, _ string, _ int64, _ string) (string, error) {
	return "mock:" + uuid.NewString(), nil
}
