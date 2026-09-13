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

// RecordUsage records one usage event. With a Redis counter configured it
// increments a buffered bucket (flushed to the aggregate table); otherwise it
// inserts a usage_events row directly. Callers treat it as best-effort and log
// the returned error without failing the chat turn.
func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error {
	if s.counter != nil {
		day := time.Now().UTC().Format("2006-01-02")
		return s.counter.Incr(ctx, tenantID, model, day, promptTokens, completionTokens)
	}
	return s.repos.Usage.Record(ctx, tenantID, model, promptTokens, completionTokens)
}

// UsageSummary aggregates usage for the tenant over the half-open window [from, to).
func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	if s.counter != nil {
		return s.repos.Aggregates.Summary(ctx, tenantID, from, to)
	}
	return s.repos.Usage.Summary(ctx, tenantID, from, to)
}

// UsageDaily returns per-UTC-day aggregates for the tenant over [from, to).
func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	if s.counter != nil {
		return s.repos.Aggregates.Daily(ctx, tenantID, from, to)
	}
	return s.repos.Usage.Daily(ctx, tenantID, from, to)
}

// UsageByModel aggregates usage per model over [from, to), ordered by total tokens desc.
func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	if s.counter != nil {
		return s.repos.Aggregates.ByModel(ctx, tenantID, from, to)
	}
	return s.repos.Usage.ByModel(ctx, tenantID, from, to)
}
