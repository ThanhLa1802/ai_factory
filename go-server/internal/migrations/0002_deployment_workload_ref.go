package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0002DeploymentWorkloadRef (converted from goose 0002).
func M0002DeploymentWorkloadRef() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0002_deployment_workload_ref",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE deployments ADD COLUMN workload_ref TEXT NOT NULL DEFAULT ''`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE deployments DROP COLUMN workload_ref`).Error
		},
	}
}
