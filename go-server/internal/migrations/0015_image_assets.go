package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0015ImageAssets stores every generated image (one row per PNG). The file
// itself lives on disk at `path`; this table is the metadata + tenant scoping.
func M0015ImageAssets() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0015_image_assets",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE image_assets (
				id                UUID PRIMARY KEY,
				tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				job_id            UUID,
				model             TEXT NOT NULL DEFAULT '',
				prompt            TEXT NOT NULL DEFAULT '',
				negative_prompt   TEXT NOT NULL DEFAULT '',
				width             INT  NOT NULL,
				height            INT  NOT NULL,
				steps             INT  NOT NULL,
				guidance_scale    REAL NOT NULL DEFAULT 0,
				seed              BIGINT NOT NULL DEFAULT 0,
				scheduler         TEXT NOT NULL DEFAULT '',
				path              TEXT NOT NULL,
				created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
			)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX image_assets_tenant_created_idx ON image_assets (tenant_id, created_at DESC)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS image_assets CASCADE`).Error
		},
	}
}
