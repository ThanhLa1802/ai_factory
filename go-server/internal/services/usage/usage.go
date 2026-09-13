package usage

import (
	"context"
	"time"
)

// UsageSummary aggregates token + request counts over a time window.
type UsageSummary struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Requests         int64 `json:"requests"`
}

// UsageDailyPoint is one UTC day's aggregate for the bar chart.
type UsageDailyPoint struct {
	Date             string `json:"date"` // YYYY-MM-DD (UTC)
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Requests         int64  `json:"requests"`
}

// UsageByModel is the per-model aggregate for the breakdown table.
type UsageByModel struct {
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Requests         int64  `json:"requests"`
}

// RecordUsage appends one usage event to usage_events, the append-only source
// of truth. A background Roller folds it into the usage_daily aggregate. Callers
// treat it as best-effort and log the returned error without failing the turn.
func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error {
	return s.repos.Usage.Record(ctx, tenantID, model, promptTokens, completionTokens)
}

// UsageSummary aggregates usage for the tenant over the half-open window [from, to).
func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	return s.repos.Aggregates.Summary(ctx, tenantID, from, to)
}

// UsageDaily returns per-UTC-day aggregates for the tenant over [from, to).
func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	return s.repos.Aggregates.Daily(ctx, tenantID, from, to)
}

// UsageByModel aggregates usage per model over [from, to), ordered by total tokens desc.
func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	return s.repos.Aggregates.ByModel(ctx, tenantID, from, to)
}
