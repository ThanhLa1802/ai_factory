package controlplane

import (
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
)

// TestRowsSelectable is a schema-parity guard: each GORM row type must be
// selectable against the migrated tables, so any column-name drift between the
// struct tags and the SQL schema fails loudly.
func TestRowsSelectable(t *testing.T) {
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
		{"tenant_quotas", &[]quotaRow{}},
		{"usage_events", &[]usageRow{}},
		{"idempotency_keys", &[]idempotencyRow{}},
		{"models", &[]modelRow{}},
		{"model_versions", &[]modelVersionRow{}},
		{"serving_templates", &[]templateRow{}},
		{"serving_template_versions", &[]templateVersionRow{}},
		{"deployments", &[]deploymentRow{}},
		{"deployment_revisions", &[]revisionRow{}},
		{"endpoints", &[]endpointRow{}},
	}
	for _, c := range cases {
		if err := g.Limit(1).Find(c.dest).Error; err != nil {
			t.Errorf("%s: select failed (column drift?): %v", c.name, err)
		}
	}
}
