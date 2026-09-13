package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0010BackfillUsageDaily folds pre-aggregate usage_events rows into usage_daily
// once. Before Phase 6b every turn inserted a usage_events row and reads came
// from that table; from Phase 6b on, reads come from the flushed aggregate, so
// historical rows would otherwise be invisible. Days are grouped by UTC, the
// same bucket the Redis counter writes.
func M0010BackfillUsageDaily() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0010_backfill_usage_daily",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`
				INSERT INTO usage_daily (tenant_id, model, day, prompt_tokens, completion_tokens, requests, updated_at)
				SELECT tenant_id,
				       model,
				       (created_at AT TIME ZONE 'UTC')::date AS day,
				       COALESCE(SUM(prompt_tokens), 0)::bigint,
				       COALESCE(SUM(completion_tokens), 0)::bigint,
				       COUNT(*)::bigint,
				       now()
				FROM usage_events
				GROUP BY tenant_id, model, (created_at AT TIME ZONE 'UTC')::date
				ON CONFLICT (tenant_id, model, day) DO UPDATE SET
				    prompt_tokens     = usage_daily.prompt_tokens + EXCLUDED.prompt_tokens,
				    completion_tokens = usage_daily.completion_tokens + EXCLUDED.completion_tokens,
				    requests          = usage_daily.requests + EXCLUDED.requests,
				    updated_at        = now()`).Error
		},
		// A one-time data backfill is not reversible: the aggregated rows may
		// have accumulated further flushes, so subtracting them could go
		// negative. Rolling back is a no-op; usage_daily data is additive only.
		Rollback: func(*gorm.DB) error { return nil },
	}
}
