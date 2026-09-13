package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/google/uuid"
)

// TestAggregateUpsertAccumulatesAndReads runs against a live Postgres: two
// flushes for the same (tenant, model, day) accumulate, and the summary/daily/
// by-model reads sum the aggregate table.
func TestAggregateUpsertAccumulatesAndReads(t *testing.T) {
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
	if err := d.Gorm().Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, tenantID, "agg-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec(`DELETE FROM tenants WHERE id = $1`, tenantID) })

	aggs := NewRepositories(d.Gorm()).Aggregates
	day := time.Now().UTC().Format("2006-01-02")
	bucket := cache.UsageBucket{TenantID: tenantID, Model: "qwen-3b", Day: day, PromptTokens: 10, CompletionTokens: 5, Requests: 1}
	if err := aggs.Upsert(ctx, bucket); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	bucket.PromptTokens, bucket.CompletionTokens, bucket.Requests = 20, 7, 2
	if err := aggs.Upsert(ctx, bucket); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -1)
	to := now.AddDate(0, 0, 1)

	summary, err := aggs.Summary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.PromptTokens != 30 || summary.CompletionTokens != 12 || summary.TotalTokens != 42 || summary.Requests != 3 {
		t.Fatalf("summary = %+v, want prompt=30 completion=12 total=42 requests=3", summary)
	}

	daily, err := aggs.Daily(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(daily) != 1 || daily[0].Date != day || daily[0].Requests != 3 {
		t.Fatalf("daily = %+v, want one point %s requests=3", daily, day)
	}

	byModel, err := aggs.ByModel(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if len(byModel) != 1 || byModel[0].Model != "qwen-3b" || byModel[0].TotalTokens != 42 {
		t.Fatalf("byModel = %+v, want qwen-3b total=42", byModel)
	}
}

// TestServiceBuffersThenFlushes proves the counter path: RecordUsage buffers in
// Redis (no usage_events row), reads come from the aggregate table, and a flush
// makes the buffered tokens visible.
func TestServiceBuffersThenFlushes(t *testing.T) {
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
	if err := d.Gorm().Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'ACTIVE')`, tenantID, "buf-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _ = d.Gorm().Exec(`DELETE FROM tenants WHERE id = $1`, tenantID) })

	rdb := newTestRedis(t)
	counter := cache.NewUsageCounter(rdb)
	repos := NewRepositories(d.Gorm())
	svc := NewServiceWithCounter(repos, counter)

	if err := svc.RecordUsage(ctx, tenantID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record: %v", err)
	}

	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -1), now.AddDate(0, 0, 1)

	before, err := svc.UsageSummary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary before flush: %v", err)
	}
	if before.TotalTokens != 0 {
		t.Fatalf("summary before flush = %+v, want 0 (buffered, not yet flushed)", before)
	}

	f := NewFlusher(counter, repos.Aggregates, cache.NewLock(rdb), nil)
	if n, err := f.FlushOnce(ctx); err != nil || n != 1 {
		t.Fatalf("flush = (%d,%v), want (1,nil)", n, err)
	}

	after, err := svc.UsageSummary(ctx, tenantID, from, to)
	if err != nil {
		t.Fatalf("summary after flush: %v", err)
	}
	if after.PromptTokens != 10 || after.CompletionTokens != 5 || after.Requests != 1 {
		t.Fatalf("summary after flush = %+v, want prompt=10 completion=5 requests=1", after)
	}
}
