package iam

import (
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
)

// TestIAMRowsSelectable is a schema-parity guard: each IAM GORM row type must be
// selectable against the migrated tables, so column-name drift fails loudly.
func TestIAMRowsSelectable(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(db.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	g := db.Gorm()

	cases := []struct {
		name string
		dest any
	}{
		{"tenants", &[]tenantRow{}},
		{"users", &[]userRow{}},
		{"tenant_memberships", &[]membershipRow{}},
		{"api_keys", &[]apiKeyRow{}},
	}
	for _, c := range cases {
		if err := g.Limit(1).Find(c.dest).Error; err != nil {
			t.Errorf("%s: select failed (column drift?): %v", c.name, err)
		}
	}
}
