package observability

import (
	"testing"
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
