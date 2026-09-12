package usage

import (
	"context"
	"fmt"
	"time"

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
