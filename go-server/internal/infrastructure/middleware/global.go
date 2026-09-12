// Package middleware holds the cross-cutting Gin middleware: the global
// request chain (recovery, CORS, tracing, logging, metrics) and the per-route
// auth port (design §4.1, D-P4-3). It depends only on infrastructure.
package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/gin-gonic/gin"
)

// Recovery converts a panic into a 500 instead of crashing the server.
func Recovery(c *gin.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered", "err", r, "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{"code": "INTERNAL_ERROR", "message": "internal error"},
			})
		}
	}()
	c.Next()
}

// Logging logs each request as a structured JSON line.
func Logging(c *gin.Context) {
	slog.Info("http request", "method", c.Request.Method, "path", c.Request.URL.Path)
	c.Next()
}

// CORS cho phép UI tĩnh (mở file:// hoặc serve ở port khác) gọi API.
// Dev/demo nên mở toàn bộ origin; chặn preflight OPTIONS.
func CORS(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, x-session-id")
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}

// Trace continues an inbound W3C traceparent (if any) and starts a root span for
// the request. The span — and therefore the trace — ends when the handler
// returns. See observability.StartSpan/End (A6 — traces).
func Trace(c *gin.Context) {
	ctx := observability.ExtractTraceparent(c.Request.Context(), c.GetHeader(observability.TraceparentHeader))
	ctx, span := observability.StartSpan(ctx, "http "+c.Request.Method+" "+c.Request.URL.Path)
	defer span.End()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

// Metrics ghi status + duration của mỗi request vào Prometheus. Labels
// tenant/deployment/model/region được handler set trên metricWriter sau khi
// route resolve (xem observability.RouteLabelSetter); nếu handler không set thì
// chúng để trống. Đồng thời theo dõi số request đang phục vụ.
func Metrics(c *gin.Context) {
	start := time.Now()
	observability.IncInflight()
	mw := &metricWriter{ResponseWriter: c.Writer, status: http.StatusOK}
	c.Writer = mw
	c.Next()
	observability.DecInflight()
	status := strconv.Itoa(mw.status)
	observability.HTTPRequestsTotal.WithLabelValues(mw.tenant, mw.deployment, mw.model, mw.region, status).Inc()
	observability.RequestDurationSeconds.WithLabelValues(mw.tenant, mw.deployment, mw.model, mw.region, status).Observe(time.Since(start).Seconds())
}

// metricWriter bắt status code thực tế của handler (mặc định 200 khi WriteHeader
// không được gọi) và lưu serving-domain labels mà handler set sau route resolve.
// Nó nhúng gin.ResponseWriter nên vẫn thỏa http.Flusher (SSE streaming).
type metricWriter struct {
	gin.ResponseWriter
	status     int
	tenant     string
	deployment string
	model      string
	region     string
}

func (w *metricWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// SetRouteLabels implements observability.RouteLabelSetter — handler gọi sau khi
// resolve deployment để metrics ghi nhãn theo route thật.
func (w *metricWriter) SetRouteLabels(tenant, deployment, model, region string) {
	w.tenant, w.deployment, w.model, w.region = tenant, deployment, model, region
}
