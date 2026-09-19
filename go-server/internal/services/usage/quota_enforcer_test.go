package usage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeQuotaRepo struct {
	rows []Quota
	err  error
}

func (f *fakeQuotaRepo) Upsert(ctx context.Context, q *Quota) error { return nil }
func (f *fakeQuotaRepo) List(ctx context.Context, tenantID string) ([]Quota, error) {
	return f.rows, f.err
}

type fakeAggRepo struct {
	summary UsageSummary
	err     error
	calls   [][2]time.Time
}

func (f *fakeAggRepo) Rollup(ctx context.Context) (int, error) { return 0, nil }
func (f *fakeAggRepo) Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error) {
	f.calls = append(f.calls, [2]time.Time{from, to})
	return f.summary, f.err
}
func (f *fakeAggRepo) Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error) {
	return nil, nil
}
func (f *fakeAggRepo) ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error) {
	return nil, nil
}

func newEnforcer(mode string, quotas []Quota, summary UsageSummary) (*QuotaEnforcer, *fakeAggRepo) {
	aggs := &fakeAggRepo{summary: summary}
	e := NewQuotaEnforcer(Repositories{
		Quotas:     &fakeQuotaRepo{rows: quotas},
		Aggregates: aggs,
	}, QuotaConfig{Mode: mode})
	return e, aggs
}

func TestQuotaEnforcerEnforceBlocks(t *testing.T) {
	e, _ := newEnforcer(QuotaModeEnforce,
		[]Quota{{QuotaType: "tokens_per_day", LimitValue: 100, Period: "daily"}},
		UsageSummary{TotalTokens: 100})
	if err := e.Check(context.Background(), "t1"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want ErrQuotaExceeded", err)
	}
}

func TestQuotaEnforcerShadowAllows(t *testing.T) {
	e, _ := newEnforcer(QuotaModeShadow,
		[]Quota{{QuotaType: "tokens_per_day", LimitValue: 100, Period: "daily"}},
		UsageSummary{TotalTokens: 150})
	if err := e.Check(context.Background(), "t1"); err != nil {
		t.Fatalf("shadow err = %v, want nil", err)
	}
}

func TestQuotaEnforcerUnderLimit(t *testing.T) {
	e, _ := newEnforcer(QuotaModeEnforce,
		[]Quota{{QuotaType: "tokens", LimitValue: 100, Period: "monthly"}},
		UsageSummary{TotalTokens: 99})
	if err := e.Check(context.Background(), "t1"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestQuotaEnforcerRequestsKind(t *testing.T) {
	e, _ := newEnforcer(QuotaModeEnforce,
		[]Quota{{QuotaType: "requests_per_day", LimitValue: 5, Period: "daily"}},
		UsageSummary{TotalTokens: 1_000_000, Requests: 5})
	if err := e.Check(context.Background(), "t1"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want ErrQuotaExceeded (requests)", err)
	}
}

func TestQuotaEnforcerSkipsUnknownAndZero(t *testing.T) {
	e, aggs := newEnforcer(QuotaModeEnforce,
		[]Quota{
			{QuotaType: "concurrent_requests", LimitValue: 1, Period: "daily"},
			{QuotaType: "gpu_hours", LimitValue: 10, Period: "monthly"},
			{QuotaType: "tokens", LimitValue: 0, Period: "daily"},
		},
		UsageSummary{TotalTokens: 999})
	if err := e.Check(context.Background(), "t1"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(aggs.calls) != 0 {
		t.Fatalf("Summary calls = %d, want 0", len(aggs.calls))
	}
}

func TestQuotaEnforcerListError(t *testing.T) {
	e := NewQuotaEnforcer(Repositories{Quotas: &fakeQuotaRepo{err: errors.New("db down")}}, QuotaConfig{Mode: QuotaModeEnforce})
	if err := e.Check(context.Background(), "t1"); err == nil || errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want repository error", err)
	}
}

func TestPeriodWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 15, 30, 0, 0, time.UTC)

	from, to := periodWindow("daily", now)
	if !from.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) || !to.Equal(now) {
		t.Fatalf("daily window = [%v, %v]", from, to)
	}

	from, to = periodWindow("monthly", now)
	if !from.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !to.Equal(now) {
		t.Fatalf("monthly window = [%v, %v]", from, to)
	}

	// Unknown period falls back to daily.
	from, _ = periodWindow("weekly", now)
	if !from.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly fallback from = %v", from)
	}
}
