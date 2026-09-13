package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0011UsageRollupState adds the single-row watermark that tracks how far
// usage_events has been folded into the usage_daily rollup. It initialises to
// the current max(id) because migration 0010 already backfilled those rows, so
// the roller only processes events that arrive after this point.
func M0011UsageRollupState() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0011_usage_rollup_state",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE usage_rollup_state (
				id            SMALLINT PRIMARY KEY,
				last_event_id BIGINT NOT NULL DEFAULT 0,
				updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
				CONSTRAINT usage_rollup_state_single_row CHECK (id = 1)
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO usage_rollup_state (id, last_event_id)
				VALUES (1, COALESCE((SELECT MAX(id) FROM usage_events), 0))`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS usage_rollup_state`).Error
		},
	}
}
