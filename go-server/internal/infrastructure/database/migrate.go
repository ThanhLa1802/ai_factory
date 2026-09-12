package database

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"

	"github.com/ai-factory/go-server/internal/migrations"
)

// MigrationTable is the gormigrate bookkeeping table (distinct from goose).
const MigrationTable = "schema_migrations"

// Migrate brings the schema up to date. On a database that was previously
// migrated with goose, it adopts the existing schema first (marking the
// equivalent migrations as applied) so no DDL is re-run against live data.
func Migrate(db *gorm.DB) error {
	opts := *gormigrate.DefaultOptions
	opts.TableName = MigrationTable
	m := gormigrate.New(db, &opts, migrations.All())
	if err := adoptGoose(db); err != nil {
		return err
	}
	return m.Migrate()
}

// adoptGoose is a no-op unless a goose_db_version table exists and our
// gormigrate table is still empty. It maps goose numeric versions to our
// migration IDs and records them as already applied.
func adoptGoose(db *gorm.DB) error {
	var hasGoose bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'goose_db_version')`,
	).Scan(&hasGoose).Error; err != nil {
		return fmt.Errorf("check goose table: %w", err)
	}
	if !hasGoose {
		return nil // fresh database — gormigrate runs every migration
	}
	if err := db.Exec(
		"CREATE TABLE IF NOT EXISTS " + MigrationTable + " (id varchar(255) PRIMARY KEY)",
	).Error; err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var applied int64
	if err := db.Table(MigrationTable).Count(&applied).Error; err != nil {
		return fmt.Errorf("count migrations: %w", err)
	}
	if applied > 0 {
		return nil // already adopted or already migrated by gormigrate
	}
	var versions []int64
	if err := db.Raw(
		`SELECT version_id FROM goose_db_version WHERE is_applied = true AND version_id > 0`,
	).Scan(&versions).Error; err != nil {
		return fmt.Errorf("read goose versions: %w", err)
	}
	for _, v := range versions {
		id := gooseVersionToID(v)
		if id == "" {
			continue
		}
		if err := db.Exec(
			"INSERT INTO "+MigrationTable+" (id) VALUES (?) ON CONFLICT DO NOTHING", id,
		).Error; err != nil {
			return fmt.Errorf("adopt %s: %w", id, err)
		}
	}
	return nil
}

// gooseVersionIDs maps the numeric goose versions (0001–0007, no 0005) to the
// gormigrate IDs used by this project.
var gooseVersionIDs = map[int64]string{
	1: "0001_init",
	2: "0002_deployment_workload_ref",
	3: "0003_idempotency_keys",
	4: "0004_chat_history",
	6: "0006_usage_events",
	7: "0007_session_title",
}

// gooseVersionToID returns the gormigrate ID for a goose version, or "" if the
// version has no equivalent migration.
func gooseVersionToID(v int64) string { return gooseVersionIDs[v] }
