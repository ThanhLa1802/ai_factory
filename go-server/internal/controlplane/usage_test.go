package controlplane

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/db"
	"github.com/google/uuid"
)

// TestRecordAndQueryUsage runs against a live Postgres (set AI_FACTORY_DATABASE_URL),
// mirroring the other controlplane integration tests.
func TestRecordAndQueryUsage(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewService(d.Pool())
	tenant, err := svc.CreateTenant(ctx, "usage-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	now := time.Now().UTC()
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record #1: %v", err)
	}
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen-3b", 0, 0); err != nil {
		t.Fatalf("record #2: %v", err)
	}
	if err := svc.RecordUsage(ctx, tenant.ID, "qwen3.5-9b", 100, 50); err != nil {
		t.Fatalf("record #3: %v", err)
	}

	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	summary, err := svc.UsageSummary(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.PromptTokens != 110 || summary.CompletionTokens != 55 || summary.TotalTokens != 165 || summary.Requests != 3 {
		t.Fatalf("summary = %+v, want prompt=110 completion=55 total=165 requests=3", summary)
	}

	daily, err := svc.UsageDaily(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("daily = %+v, want exactly 1 day", daily)
	}
	if daily[0].PromptTokens != 110 || daily[0].Requests != 3 {
		t.Fatalf("daily[0] = %+v, want prompt=110 requests=3", daily[0])
	}

	byModel, err := svc.UsageByModel(ctx, tenant.ID, from, to)
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("byModel = %+v, want 2 models", byModel)
	}
	// Sorted by total tokens desc → qwen3.5-9b first.
	if byModel[0].Model != "qwen3.5-9b" || byModel[0].TotalTokens != 150 {
		t.Fatalf("byModel[0] = %+v, want qwen3.5-9b total=150", byModel[0])
	}

	// Cross-tenant isolation: a different tenant sees nothing.
	other, _ := svc.CreateTenant(ctx, "usage-other-"+uuid.NewString()[:8])
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, other.ID) })
	empty, err := svc.UsageSummary(ctx, other.ID, from, to)
	if err != nil {
		t.Fatalf("empty summary: %v", err)
	}
	if empty.Requests != 0 || empty.TotalTokens != 0 {
		t.Fatalf("empty summary = %+v, want zero", empty)
	}
}
