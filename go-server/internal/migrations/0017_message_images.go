package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0017MessageImages adds the image references attached to a chat message.
// The image bytes live on disk (workspace/images/uploads); this column stores
// only [{id, mime_type, size}] so the messages table stays small.
func M0017MessageImages() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0017_message_images",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE messages
				ADD COLUMN images JSONB NOT NULL DEFAULT '[]'`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE messages DROP COLUMN IF EXISTS images`).Error
		},
	}
}
