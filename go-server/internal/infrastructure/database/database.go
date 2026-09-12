// Package database owns the GORM connection and the migration runner for the
// control plane. Only this package knows the concrete Postgres driver.
package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB wraps a GORM handle plus the underlying *sql.DB (for Close/pool stats).
type DB struct {
	gorm *gorm.DB
	sql  *sql.DB
}

// Open connects to Postgres, configures GORM, and verifies the connection.
func Open(dsn string) (*DB, error) {
	gormLogger := logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
		SlowThreshold:             time.Second,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		Colorful:                  false,
	})
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("open gorm: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("sql db: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{gorm: gdb, sql: sqlDB}, nil
}

// Gorm exposes the underlying *gorm.DB for repositories.
func (d *DB) Gorm() *gorm.DB { return d.gorm }

// Close releases the underlying connection pool (DI Closer lifecycle).
func (d *DB) Close() error { return d.sql.Close() }
