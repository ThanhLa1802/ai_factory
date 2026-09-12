package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMetricsMiddlewareGin verifies the Gin middleware increments the request
// counter with the real HTTP status code and passes the response through.
func TestMetricsMiddlewareGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(metricsMiddleware)
	e.GET("/some-path", func(c *gin.Context) { c.Status(http.StatusTeapot) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some-path", nil))

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

// metricWriter phải thỏa http.Flusher để SSE streaming hoạt động qua Gin.
func TestMetricWriterImplementsFlusher(t *testing.T) {
	// Compile-time: metricWriter có Flush (nhúng gin.ResponseWriter).
	var _ http.Flusher = (*metricWriter)(nil)

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(metricsMiddleware)
	var isFlusher bool
	e.GET("/sse", func(c *gin.Context) {
		_, isFlusher = c.Writer.(http.Flusher)
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if !isFlusher {
		t.Fatal("c.Writer must implement http.Flusher so SSE streaming survives the metrics middleware")
	}
}
