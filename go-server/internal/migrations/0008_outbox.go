package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0008Outbox creates the transactional outbox table (Phase 6). Rows are written
// in the same transaction as the domain change; a publisher drains them to the
// event bus and stamps published_at.
func M0008Outbox() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0008_outbox",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE outbox (
				id           UUID PRIMARY KEY,
				topic        TEXT NOT NULL,
				event_id     TEXT NOT NULL,
				event_type   TEXT NOT NULL,
				tenant_id    TEXT NOT NULL,
				resource_id  TEXT NOT NULL,
				event        JSONB NOT NULL,
				created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
				published_at TIMESTAMPTZ,
				attempts     INT NOT NULL DEFAULT 0,
				last_error   TEXT NOT NULL DEFAULT ''
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS outbox CASCADE`).Error
		},
	}
}
