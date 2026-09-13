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

// PoolConfig bounds the Postgres connection pool so a traffic spike cannot open
// an unbounded number of connections. Zero fields keep database/sql defaults.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig is the budget used when config does not override it.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    25,
		MaxIdleConns:    25,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// Open connects to Postgres, configures the connection pool, and verifies the
// connection. SkipDefaultTransaction drops GORM's implicit BEGIN/COMMIT around
// single-statement writes; the rollup/outbox paths use explicit db.Transaction.
// With no PoolConfig it applies DefaultPoolConfig.
func Open(dsn string, pools ...PoolConfig) (*DB, error) {
	pool := DefaultPoolConfig()
	if len(pools) > 0 {
		pool = pools[0]
	}
	gormLogger := logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
		SlowThreshold:             time.Second,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		Colorful:                  false,
	})
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 gormLogger,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open gorm: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("sql db: %w", err)
	}
	if pool.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	}
	if pool.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
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
