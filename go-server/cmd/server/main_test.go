package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-factory/go-server/internal/observability"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMetricsMiddleware verifies the middleware increments the request counter
// with the real HTTP status code and passes the response through unchanged.
func TestMetricsMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := metricsMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/some-path", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	counter, err := observability.HTTPRequestsTotal.GetMetricWithLabelValues("", "", "", "", "418")
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	if got := testutil.ToFloat64(counter); got < 1 {
		t.Fatalf("serving_requests_total{status=418} = %v, want >= 1", got)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status passthrough = %d, want %d", rec.Code, http.StatusTeapot)
	}
}
