package database

import (
	"os"
	"testing"
)

func TestGooseVersionToID(t *testing.T) {
	cases := map[int64]string{
		1: "0001_init",
		4: "0004_chat_history",
		6: "0006_usage_events",
		7: "0007_session_title",
		5: "", // no 0005 in this project
		9: "",
	}
	for v, want := range cases {
		if got := gooseVersionToID(v); got != want {
			t.Errorf("gooseVersionToID(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(db.Gorm()); err != nil {
		t.Fatalf("migrate (1st): %v", err)
	}
	if err := Migrate(db.Gorm()); err != nil {
		t.Fatalf("migrate (2nd): %v", err)
	}
	for _, table := range []string{"tenants", "messages", "usage_events", MigrationTable} {
		if !db.Gorm().Migrator().HasTable(table) {
			t.Errorf("table %q missing after migrate", table)
		}
	}
}
