package usage

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Repository interfaces. The service layer depends on these; only the
// implementations in this package know GORM.

type QuotaRepository interface {
	Upsert(ctx context.Context, q *Quota) error
	List(ctx context.Context, tenantID string) ([]Quota, error)
}

// UsageRepository is the append-only source of truth (usage_events).
type UsageRepository interface {
	Record(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
}

// AggregateRepository reads the usage_daily rollup and folds new usage_events
// into it (Rollup). Reads served to the API come from here.
type AggregateRepository interface {
	Rollup(ctx context.Context) (int, error)
	Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)
	Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)
	ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)
}

// Repositories bundles every repository the usage/quota service needs.
type Repositories struct {
	Quotas     QuotaRepository
	Usage      UsageRepository
	Aggregates AggregateRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{
		Quotas:     &quotaRepo{db: db},
		Usage:      &usageRepo{db: db},
		Aggregates: &aggregateRepo{db: db},
	}
}
