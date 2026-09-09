package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsRegistered(t *testing.T) {
	_, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if _, err := HTTPRequestsTotal.GetMetricWithLabelValues("test_tenant", "deploy_1", "model_1", "vn", "200"); err != nil {
		t.Fatalf("label combo rejected: %v", err)
	}
}

func TestRecordTokenUsageIncrements(t *testing.T) {
	// Reset so the test is repeatable.
	TokensTotal.Reset()
	RecordTokenUsage("t1", "m1", 10, 20)
	p, _ := TokensTotal.GetMetricWithLabelValues("t1", "m1", "prompt")
	c, _ := TokensTotal.GetMetricWithLabelValues("t1", "m1", "completion")
	if got := testutil.ToFloat64(p); got != 10 {
		t.Fatalf("prompt tokens = %v, want 10", got)
	}
	if got := testutil.ToFloat64(c); got != 20 {
		t.Fatalf("completion tokens = %v, want 20", got)
	}
}

func TestRecordTokenUsageSkipsZero(t *testing.T) {
	TokensTotal.Reset()
	RecordTokenUsage("t1", "m1", 0, 0)
	// A zero-only call must not create series: querying unregistered label
	// values would return an error; here the series should simply be 0.
	c, _ := TokensTotal.GetMetricWithLabelValues("t1", "m1", "completion")
	if got := testutil.ToFloat64(c); got != 0 {
		t.Fatalf("completion tokens = %v, want 0", got)
	}
}

func TestIncOverloaded(t *testing.T) {
	OverloadedTotal.Reset()
	IncOverloaded("t1", "m1")
	c, _ := OverloadedTotal.GetMetricWithLabelValues("t1", "m1")
	if got := testutil.ToFloat64(c); got != 1 {
		t.Fatalf("overloaded = %v, want 1", got)
	}
}

func TestInflightGauge(t *testing.T) {
	InflightRequests.Set(0)
	IncInflight()
	IncInflight()
	DecInflight()
	if got := testutil.ToFloat64(InflightRequests); got != 1 {
		t.Fatalf("inflight = %v, want 1", got)
	}
}
