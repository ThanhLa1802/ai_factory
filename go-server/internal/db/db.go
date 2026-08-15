package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
	dsn  string
}

// Connect opens a pgx pool and verifies the connection.
func Connect(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool, dsn: dsn}, nil
}

// Pool exposes the underlying pgx pool.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Migrate applies all embedded goose migrations.
func (d *DB) Migrate(ctx context.Context) error {
	sqlDB, err := sql.Open("pgx", d.dsn)
	if err != nil {
		return fmt.Errorf("open sql db: %w", err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrationsFS, goose.WithVerbose(false))
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
