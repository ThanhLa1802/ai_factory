package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0003IdempotencyKeys (converted from goose 0003).
func M0003IdempotencyKeys() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0003_idempotency_keys",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE idempotency_keys (
				id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				key           TEXT NOT NULL,
				resource_type TEXT NOT NULL,
				resource_id   UUID NOT NULL,
				created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE (tenant_id, key)
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX idempotency_keys_resource ON idempotency_keys (resource_type, resource_id)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS idempotency_keys`).Error
		},
	}
}
