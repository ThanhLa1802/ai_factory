package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/google/uuid"
)

// TestUsageRollupPipeline runs against a live Postgres: usage_events is the
// append-only source of truth, reads come from the usage_daily rollup, and the
// watermark-driven rollup is idempotent (re-rolling with no new events adds
// nothing, new events accumulate).
func TestUsageRollupPipeline(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tenantID := uuid.NewString()
	if err := d.Gorm().Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, tenantID, "roll-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec(`DELETE FROM tenants WHERE id = $1`, tenantID) })

	svc := NewServiceFromGorm(d.Gorm())
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -1), now.AddDate(0, 0, 1)

	if err := svc.RecordUsage(ctx, tenantID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if err := svc.RecordUsage(ctx, tenantID, "qwen-3b", 20, 10); err != nil {
		t.Fatalf("record 2: %v", err)
	}

	// Reads come from the rollup, so nothing is visible before the first roll.
	before, err := svc.UsageSummary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary before roll: %v", err)
	}
	if before.TotalTokens != 0 || before.Requests != 0 {
		t.Fatalf("summary before roll = %+v, want zero", before)
	}

	if n, err := svc.Rollup(ctx); err != nil || n < 1 {
		t.Fatalf("roll 1 = (%d,%v), want >=1,nil", n, err)
	}
	summary, err := svc.UsageSummary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary after roll: %v", err)
	}
	if summary.PromptTokens != 30 || summary.CompletionTokens != 15 || summary.TotalTokens != 45 || summary.Requests != 2 {
		t.Fatalf("summary = %+v, want prompt=30 completion=15 total=45 requests=2", summary)
	}

	// Re-rolling with no new events is a no-op (watermark unchanged).
	if n, err := svc.Rollup(ctx); err != nil || n != 0 {
		t.Fatalf("roll 2 = (%d,%v), want 0,nil (idempotent)", n, err)
	}
	if err := svc.RecordUsage(ctx, tenantID, "qwen-3b", 1, 1); err != nil {
		t.Fatalf("record 3: %v", err)
	}
	if n, err := svc.Rollup(ctx); err != nil || n < 1 {
		t.Fatalf("roll 3 = (%d,%v), want >=1,nil", n, err)
	}
	after, err := svc.UsageSummary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary after roll 3: %v", err)
	}
	if after.Requests != 3 || after.TotalTokens != 47 {
		t.Fatalf("summary after roll 3 = %+v, want requests=3 total=47", after)
	}

	byModel, err := svc.UsageByModel(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if len(byModel) != 1 || byModel[0].Model != "qwen-3b" || byModel[0].TotalTokens != 47 {
		t.Fatalf("byModel = %+v, want qwen-3b total=47", byModel)
	}
}
