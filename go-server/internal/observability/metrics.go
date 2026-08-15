package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registry = prometheus.NewRegistry()

	// HTTPRequestsTotal counts inference + control plane requests.
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "serving_requests_total",
			Help: "Total HTTP requests served.",
		},
		[]string{"tenant", "deployment", "model", "region", "status"},
	)

	// RequestDurationSeconds measures handler latency.
	RequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "serving_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tenant", "deployment", "model", "region", "status"},
	)
)

func init() {
	registry.MustRegister(HTTPRequestsTotal, RequestDurationSeconds)
}

// Registry exposes the app Prometheus registry.
func Registry() *prometheus.Registry { return registry }

// MetricsHandler returns an HTTP handler exposing /metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
