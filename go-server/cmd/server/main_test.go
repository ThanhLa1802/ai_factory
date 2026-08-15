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

// statusRecorder phải thỏa http.Flusher để SSE streaming hoạt động.
// Trước fix, statusRecorder embed http.ResponseWriter (interface không khai báo
// Flush) nên w.(http.Flusher) trong NewSSEWriter fail → 500 "streaming not supported".
func TestStatusRecorderImplementsFlusher(t *testing.T) {
	// Compile-time: statusRecorder giờ có Flush() delegate.
	var _ http.Flusher = (*statusRecorder)(nil)

	// Runtime, đúng path NewSSEWriter: assertion qua biến kiểu http.ResponseWriter.
	rec := httptest.NewRecorder()
	var w http.ResponseWriter = &statusRecorder{ResponseWriter: rec}
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder must implement http.Flusher so SSE streaming survives the metrics middleware")
	}
	flusher.Flush()
}
