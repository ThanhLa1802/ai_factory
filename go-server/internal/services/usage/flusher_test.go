package usage

import (
	"context"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeAggs is an in-memory AggregateRepository that accumulates upserts the way
// the SQL ON CONFLICT does.
type fakeAggs struct{ m map[string]cache.UsageBucket }

func newFakeAggs() *fakeAggs { return &fakeAggs{m: map[string]cache.UsageBucket{}} }

func aggKey(b cache.UsageBucket) string { return b.TenantID + "|" + b.Model + "|" + b.Day }

func (a *fakeAggs) Upsert(_ context.Context, b cache.UsageBucket) error {
	cur := a.m[aggKey(b)]
	cur.TenantID, cur.Model, cur.Day = b.TenantID, b.Model, b.Day
	cur.PromptTokens += b.PromptTokens
	cur.CompletionTokens += b.CompletionTokens
	cur.Requests += b.Requests
	a.m[aggKey(b)] = cur
	return nil
}

func (a *fakeAggs) Summary(context.Context, string, time.Time, time.Time) (UsageSummary, error) {
	return UsageSummary{}, nil
}
func (a *fakeAggs) Daily(context.Context, string, time.Time, time.Time) ([]UsageDailyPoint, error) {
	return nil, nil
}
func (a *fakeAggs) ByModel(context.Context, string, time.Time, time.Time) ([]UsageByModel, error) {
	return nil, nil
}

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestFlusherDrainsAndAccumulates(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	counter := cache.NewUsageCounter(rdb)
	aggs := newFakeAggs()
	f := NewFlusher(counter, aggs, cache.NewLock(rdb), nil)

	if err := counter.Incr(ctx, "t1", "qwen-3b", "2026-09-13", 10, 5); err != nil {
		t.Fatalf("incr: %v", err)
	}
	n, err := f.FlushOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("flush = (%d,%v), want (1,nil)", n, err)
	}
	// A second batch for the same bucket accumulates on top.
	if err := counter.Incr(ctx, "t1", "qwen-3b", "2026-09-13", 20, 7); err != nil {
		t.Fatalf("incr 2: %v", err)
	}
	if n, err := f.FlushOnce(ctx); err != nil || n != 1 {
		t.Fatalf("flush 2 = (%d,%v), want (1,nil)", n, err)
	}
	got := aggs.m["t1|qwen-3b|2026-09-13"]
	if got.PromptTokens != 30 || got.CompletionTokens != 12 || got.Requests != 2 {
		t.Fatalf("bucket = %+v, want prompt=30 completion=12 requests=2", got)
	}
}

func TestFlusherSkipsWhenLockHeld(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	counter := cache.NewUsageCounter(rdb)
	aggs := newFakeAggs()
	lock := cache.NewLock(rdb)
	f := NewFlusher(counter, aggs, lock, nil)

	// Another replica holds the flush lock.
	if _, ok, err := lock.Acquire(ctx, flushLockKey, time.Minute); err != nil || !ok {
		t.Fatalf("hold lock = (%v,%v)", ok, err)
	}
	if err := counter.Incr(ctx, "t1", "m", "2026-09-13", 1, 1); err != nil {
		t.Fatalf("incr: %v", err)
	}

	n, err := f.FlushOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("flush while locked = (%d,%v), want (0,nil)", n, err)
	}
	if len(aggs.m) != 0 {
		t.Fatalf("aggregate written while locked: %+v", aggs.m)
	}
}
