package cache

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestUsageCounter(t *testing.T) *UsageCounter {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewUsageCounter(rdb)
}

func TestUsageCounterIncrAndDrain(t *testing.T) {
	ctx := context.Background()
	c := newTestUsageCounter(t)

	if err := c.Incr(ctx, "t1", "qwen-3b", "2026-09-13", 10, 5); err != nil {
		t.Fatalf("incr 1: %v", err)
	}
	if err := c.Incr(ctx, "t1", "qwen-3b", "2026-09-13", 20, 7); err != nil {
		t.Fatalf("incr 2: %v", err)
	}
	if err := c.Incr(ctx, "t1", "qwen3.5-9b", "2026-09-13", 1, 1); err != nil {
		t.Fatalf("incr other model: %v", err)
	}

	buckets, err := c.Drain(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("drained %d buckets, want 2", len(buckets))
	}
	byModel := map[string]UsageBucket{}
	for _, b := range buckets {
		byModel[b.Model] = b
	}
	q := byModel["qwen-3b"]
	if q.PromptTokens != 30 || q.CompletionTokens != 12 || q.Requests != 2 {
		t.Fatalf("qwen-3b bucket = %+v, want prompt=30 completion=12 requests=2", q)
	}
	if q.TenantID != "t1" || q.Day != "2026-09-13" {
		t.Fatalf("qwen-3b key fields = %+v", q)
	}

	// A second drain is empty: the keys were removed.
	again, err := c.Drain(ctx)
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second drain = %d buckets, want 0", len(again))
	}
}
