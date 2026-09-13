package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- quotas ---

type quotaRepo struct{ db *gorm.DB }

func (r *quotaRepo) Upsert(ctx context.Context, q *Quota) error {
	row := quotaRow{
		ID: q.ID, TenantID: q.TenantID, QuotaType: q.QuotaType,
		LimitValue: q.LimitValue, Period: q.Period,
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "quota_type"}, {Name: "period"}},
		DoUpdates: clause.AssignmentColumns([]string{"limit_value", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert quota: %w", err)
	}
	// ON CONFLICT keeps the existing row's id; read it back so the returned
	// Quota matches the stored one.
	var id string
	if err := r.db.WithContext(ctx).Model(&quotaRow{}).
		Where("tenant_id = ? AND quota_type = ? AND period = ?", q.TenantID, q.QuotaType, q.Period).
		Select("id").Scan(&id).Error; err != nil {
		return fmt.Errorf("upsert quota id: %w", err)
	}
	q.ID = id
	return nil
}

func (r *quotaRepo) List(ctx context.Context, tenantID string) ([]Quota, error) {
	var rows []quotaRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("quota_type").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list quotas: %w", err)
	}
	out := make([]Quota, 0, len(rows))
	for _, row := range rows {
		out = append(out, toQuota(row))
	}
	return out, nil
}

// --- usage ---

type usageRepo struct{ db *gorm.DB }

func (r *usageRepo) Record(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error {
	row := usageRow{
		TenantID: tenantID, Model: model,
		PromptTokens: promptTokens, CompletionTokens: completionTokens,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

func (r *usageRepo) Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	var out UsageSummary
	err := r.db.WithContext(ctx).Model(&usageRow{}).
		Select(`COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8 AS total_tokens,
		        COUNT(*)::int8 AS requests`).
		Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, from, to).
		Scan(&out).Error
	if err != nil {
		return out, fmt.Errorf("usage summary: %w", err)
	}
	return out, nil
}

func (r *usageRepo) Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	var rows []UsageDailyPoint
	err := r.db.WithContext(ctx).Model(&usageRow{}).
		Select(`(created_at AT TIME ZONE 'UTC')::date::text AS date,
		        COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COUNT(*)::int8 AS requests`).
		Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, from, to).
		Group("(created_at AT TIME ZONE 'UTC')::date").
		Order("date").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("usage daily: %w", err)
	}
	if rows == nil {
		rows = []UsageDailyPoint{}
	}
	return rows, nil
}

func (r *usageRepo) ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	var rows []UsageByModel
	err := r.db.WithContext(ctx).Model(&usageRow{}).
		Select(`model,
		        COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8 AS total_tokens,
		        COUNT(*)::int8 AS requests`).
		Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, from, to).
		Group("model").
		Order("COALESCE(SUM(prompt_tokens + completion_tokens),0) DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	if rows == nil {
		rows = []UsageByModel{}
	}
	return rows, nil
}

// --- usage aggregate (flushed from Redis counters) ---

type aggregateRepo struct{ db *gorm.DB }

func (r *aggregateRepo) Upsert(ctx context.Context, b cache.UsageBucket) error {
	day, err := time.Parse("2006-01-02", b.Day)
	if err != nil {
		return fmt.Errorf("parse usage day %q: %w", b.Day, err)
	}
	row := usageDailyRow{
		TenantID: b.TenantID, Model: b.Model, Day: day,
		PromptTokens: b.PromptTokens, CompletionTokens: b.CompletionTokens, Requests: b.Requests,
	}
	// Accumulate: a later flush for the same (tenant, model, day) adds to the
	// existing row instead of overwriting it.
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "model"}, {Name: "day"}},
		DoUpdates: clause.Assignments(map[string]any{
			"prompt_tokens":     gorm.Expr("usage_daily.prompt_tokens + EXCLUDED.prompt_tokens"),
			"completion_tokens": gorm.Expr("usage_daily.completion_tokens + EXCLUDED.completion_tokens"),
			"requests":          gorm.Expr("usage_daily.requests + EXCLUDED.requests"),
			"updated_at":        time.Now().UTC(),
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert usage daily: %w", err)
	}
	return nil
}

func (r *aggregateRepo) Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	var out UsageSummary
	err := r.db.WithContext(ctx).Model(&usageDailyRow{}).
		Select(`COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8 AS total_tokens,
		        COALESCE(SUM(requests),0)::int8 AS requests`).
		Where("tenant_id = ? AND day >= ?::date AND day <= ?::date", tenantID, from, to).
		Scan(&out).Error
	if err != nil {
		return out, fmt.Errorf("usage daily summary: %w", err)
	}
	return out, nil
}

func (r *aggregateRepo) Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	var rows []UsageDailyPoint
	err := r.db.WithContext(ctx).Model(&usageDailyRow{}).
		Select(`to_char(day, 'YYYY-MM-DD') AS date,
		        COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COALESCE(SUM(requests),0)::int8 AS requests`).
		Where("tenant_id = ? AND day >= ?::date AND day <= ?::date", tenantID, from, to).
		Group("day").
		Order("day").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("usage daily chart: %w", err)
	}
	if rows == nil {
		rows = []UsageDailyPoint{}
	}
	return rows, nil
}

func (r *aggregateRepo) ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	var rows []UsageByModel
	err := r.db.WithContext(ctx).Model(&usageDailyRow{}).
		Select(`model,
		        COALESCE(SUM(prompt_tokens),0)::int8 AS prompt_tokens,
		        COALESCE(SUM(completion_tokens),0)::int8 AS completion_tokens,
		        COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8 AS total_tokens,
		        COALESCE(SUM(requests),0)::int8 AS requests`).
		Where("tenant_id = ? AND day >= ?::date AND day <= ?::date", tenantID, from, to).
		Group("model").
		Order("COALESCE(SUM(prompt_tokens + completion_tokens),0) DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("usage daily by model: %w", err)
	}
	if rows == nil {
		rows = []UsageByModel{}
	}
	return rows, nil
}
