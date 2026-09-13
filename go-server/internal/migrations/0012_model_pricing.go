package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0012ModelPricing creates the per-model price table (prepaid billing). Prices
// are integer micro-credits per 1,000,000 tokens (input and output priced
// separately); money never uses float. Keyed by the model *name* recorded in
// usage_events/usage_daily.
func M0012ModelPricing() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0012_model_pricing",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE model_pricing (
				id                              UUID PRIMARY KEY,
				model                           TEXT NOT NULL,
				currency                        TEXT NOT NULL DEFAULT 'USD',
				price_per_million_input_tokens  BIGINT NOT NULL DEFAULT 0,
				price_per_million_output_tokens BIGINT NOT NULL DEFAULT 0,
				created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE (model, currency)
			)`).Error; err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS model_pricing CASCADE`).Error
		},
	}
}
