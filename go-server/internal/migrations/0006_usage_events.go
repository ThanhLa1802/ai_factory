package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0006UsageEvents (converted from goose 0006).
func M0006UsageEvents() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0006_usage_events",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE usage_events (
				id                BIGSERIAL PRIMARY KEY,
				tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				model             TEXT NOT NULL DEFAULT '',
				prompt_tokens     INT  NOT NULL DEFAULT 0,
				completion_tokens INT  NOT NULL DEFAULT 0,
				created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX usage_events_tenant_created_idx ON usage_events (tenant_id, created_at DESC)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS usage_events CASCADE`).Error
		},
	}
}
