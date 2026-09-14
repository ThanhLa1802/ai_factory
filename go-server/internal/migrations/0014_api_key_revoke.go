package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0014APIKeyRevoke adds revoked_at to api_keys so a key is soft-revoked
// (status=REVOKED, row retained for audit) instead of hard-deleted.
func M0014APIKeyRevoke() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0014_api_key_revoke",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE api_keys ADD COLUMN revoked_at TIMESTAMPTZ`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE api_keys DROP COLUMN IF EXISTS revoked_at`).Error
		},
	}
}
