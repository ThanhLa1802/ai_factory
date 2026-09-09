package controlplane

import (
	"context"
	"fmt"
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

// RecordUsage inserts one usage event. Callers treat this as best-effort and
// log the returned error without failing the chat turn.
func (s *Service) RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO usage_events (tenant_id, model, prompt_tokens, completion_tokens)
		VALUES ($1::uuid, $2, $3::int, $4::int)`,
		tenantID, model, promptTokens, completionTokens)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// UsageSummary aggregates usage for the tenant over the half-open window [from, to).
func (s *Service) UsageSummary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	var out UsageSummary
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3`,
		tenantID, from, to).
		Scan(&out.PromptTokens, &out.CompletionTokens, &out.TotalTokens, &out.Requests)
	if err != nil {
		return out, fmt.Errorf("usage summary: %w", err)
	}
	return out, nil
}

// UsageDaily returns per-UTC-day aggregates for the tenant over [from, to).
func (s *Service) UsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	rows, err := s.db.Query(ctx, `
		SELECT (created_at AT TIME ZONE 'UTC')::date::text AS d,
		       COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY d
		ORDER BY d`, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage daily: %w", err)
	}
	defer rows.Close()
	out := []UsageDailyPoint{}
	for rows.Next() {
		var p UsageDailyPoint
		if err := rows.Scan(&p.Date, &p.PromptTokens, &p.CompletionTokens, &p.Requests); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UsageByModel aggregates usage per model over [from, to), ordered by total tokens desc.
func (s *Service) UsageByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	rows, err := s.db.Query(ctx, `
		SELECT model,
		       COALESCE(SUM(prompt_tokens),0)::int8,
		       COALESCE(SUM(completion_tokens),0)::int8,
		       COALESCE(SUM(prompt_tokens + completion_tokens),0)::int8,
		       COUNT(*)::int8
		FROM usage_events
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY model
		ORDER BY COALESCE(SUM(prompt_tokens + completion_tokens),0) DESC`, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	defer rows.Close()
	out := []UsageByModel{}
	for rows.Next() {
		var m UsageByModel
		if err := rows.Scan(&m.Model, &m.PromptTokens, &m.CompletionTokens, &m.TotalTokens, &m.Requests); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
