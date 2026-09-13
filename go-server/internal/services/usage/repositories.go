package usage

import (
	"context"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"gorm.io/gorm"
)

// Repository interfaces. The service layer depends on these; only the
// implementations in this package know GORM.

type QuotaRepository interface {
	Upsert(ctx context.Context, q *Quota) error
	List(ctx context.Context, tenantID string) ([]Quota, error)
}

type UsageRepository interface {
	Record(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
	Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)
	Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)
	ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)
}

// AggregateRepository reads and writes the flushed usage aggregate table.
type AggregateRepository interface {
	Upsert(ctx context.Context, b cache.UsageBucket) error
	Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)
	Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)
	ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)
}

// Counter buffers usage in Redis. Implemented by infrastructure/cache.
type Counter interface {
	Incr(ctx context.Context, tenantID, model, day string, promptTokens, completionTokens int) error
	Drain(ctx context.Context) ([]cache.UsageBucket, error)
}

// Locker is a distributed lock. Implemented by infrastructure/cache.
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error)
	Release(ctx context.Context, key, token string) error
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
