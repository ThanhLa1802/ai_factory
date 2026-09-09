package db

import (
	"context"
	"os"
	"testing"
)

// TestMigrateAndPing runs only when AI_FACTORY_DATABASE_URL is set (integration).
func TestMigrateAndPing(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := d.Pool().Ping(ctx); err != nil {
		t.Fatalf("Ping() after migrate error = %v", err)
	}
}
