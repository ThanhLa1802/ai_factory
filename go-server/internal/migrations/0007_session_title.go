package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0007SessionTitle (converted from goose 0007).
func M0007SessionTitle() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0007_session_title",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE sessions DROP COLUMN IF EXISTS title`).Error
		},
	}
}
