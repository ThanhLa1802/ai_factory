package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0009UsageDaily creates the usage aggregate table (Phase 6b). A background
// flusher drains Redis counters and upserts one row per (tenant, model, day).
func M0009UsageDaily() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0009_usage_daily",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE usage_daily (
				tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				model             TEXT NOT NULL DEFAULT '',
				day               DATE NOT NULL,
				prompt_tokens     BIGINT NOT NULL DEFAULT 0,
				completion_tokens BIGINT NOT NULL DEFAULT 0,
				requests          BIGINT NOT NULL DEFAULT 0,
				updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY (tenant_id, model, day)
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX usage_daily_tenant_day_idx ON usage_daily (tenant_id, day DESC)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS usage_daily CASCADE`).Error
		},
	}
}
