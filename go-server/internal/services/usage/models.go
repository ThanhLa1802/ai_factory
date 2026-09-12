package usage

import "time"

// GORM row types. These mirror the SQL schema exactly (table + column names)
// and are deliberately separate from the domain/JSON structs; repositories map
// between them. JSONB columns use GORM's `serializer:json`.

// --- usage / quota ---

type quotaRow struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID   string    `gorm:"column:tenant_id;type:uuid"`
	QuotaType  string    `gorm:"column:quota_type"`
	LimitValue int64     `gorm:"column:limit_value"`
	Period     string    `gorm:"column:period"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (quotaRow) TableName() string { return "tenant_quotas" }

type usageRow struct {
	ID               int64     `gorm:"column:id;primaryKey"`
	TenantID         string    `gorm:"column:tenant_id;type:uuid"`
	Model            string    `gorm:"column:model"`
	PromptTokens     int       `gorm:"column:prompt_tokens"`
	CompletionTokens int       `gorm:"column:completion_tokens"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

func (usageRow) TableName() string { return "usage_events" }

// --- mappers (row → domain) ---

func toQuota(r quotaRow) Quota {
	return Quota{ID: r.ID, TenantID: r.TenantID, QuotaType: r.QuotaType, LimitValue: r.LimitValue, Period: r.Period}
}
