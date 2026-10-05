package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0016ImageJobs stores async image-generation jobs. The runner (Phase 3) picks
// up queued rows, streams progress into progress_step, and writes the produced
// assets with job_id set.
func M0016ImageJobs() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0016_image_jobs",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE image_jobs (
				id                UUID PRIMARY KEY,
				tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				model             TEXT NOT NULL DEFAULT '',
				status            TEXT NOT NULL DEFAULT 'queued',
				prompt            TEXT NOT NULL DEFAULT '',
				negative_prompt   TEXT NOT NULL DEFAULT '',
				width             INT  NOT NULL,
				height            INT  NOT NULL,
				steps             INT  NOT NULL,
				guidance_scale    REAL NOT NULL DEFAULT 0,
				seed              BIGINT NOT NULL DEFAULT -1,
				num_images        INT  NOT NULL DEFAULT 1,
				scheduler         TEXT NOT NULL DEFAULT '',
				progress_step     INT  NOT NULL DEFAULT 0,
				error             TEXT NOT NULL DEFAULT '',
				created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
			)`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX image_jobs_tenant_created_idx ON image_jobs (tenant_id, created_at DESC)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX image_jobs_status_idx ON image_jobs (status)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS image_jobs CASCADE`).Error
		},
	}
}
