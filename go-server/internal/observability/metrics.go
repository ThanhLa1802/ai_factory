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

	// TokensTotal meters inference usage: prompt vs completion tokens per
	// tenant + model (roadmap A6 — usage metering).
	TokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "serving_tokens_total",
			Help: "Tokens consumed by inference, split by prompt vs completion.",
		},
		[]string{"tenant", "model", "type"},
	)

	// InflightRequests gauges how many requests are being served right now.
	InflightRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "serving_inflight_requests",
			Help: "Number of requests currently being served.",
		},
	)

	// OverloadedTotal counts requests shed due to backpressure (A5: TrySubmit
	// → ErrOverloaded → HTTP 503), per tenant + model.
	OverloadedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "serving_overloaded_total",
			Help: "Requests shed because the worker queue was saturated.",
		},
		[]string{"tenant", "model"},
	)
)

func init() {
	registry.MustRegister(HTTPRequestsTotal, RequestDurationSeconds, TokensTotal, InflightRequests, OverloadedTotal)
}

// Registry exposes the app Prometheus registry.
func Registry() *prometheus.Registry { return registry }

// MetricsHandler returns an HTTP handler exposing /metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// RouteLabelSetter lets an HTTP handler record serving-domain labels on the
// metrics middleware's response recorder, so serving metrics resolve after
// routing (the handler knows tenant/deployment/model/region; the middleware
// only knows the HTTP status).
type RouteLabelSetter interface {
	SetRouteLabels(tenant, deployment, model, region string)
}

// RecordTokenUsage meters prompt/completion tokens for usage tracking (A6).
// The handler calls this when a generation finishes (its final event carries
// Usage). Zero counts are skipped so a bare final event creates no series.
func RecordTokenUsage(tenant, model string, promptTokens, completionTokens int) {
	if promptTokens > 0 {
		TokensTotal.WithLabelValues(tenant, model, "prompt").Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		TokensTotal.WithLabelValues(tenant, model, "completion").Add(float64(completionTokens))
	}
}

// IncOverloaded counts a request shed by backpressure (HTTP 503 overloaded).
func IncOverloaded(tenant, model string) {
	OverloadedTotal.WithLabelValues(tenant, model).Inc()
}

// IncInflight / DecInflight track concurrent requests (a gauge around the HTTP
// middleware: Inc before ServeHTTP, Dec after it returns).
func IncInflight() { InflightRequests.Inc() }
func DecInflight() { InflightRequests.Dec() }
