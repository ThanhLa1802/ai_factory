package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"
)

// TraceparentHeader is the W3C trace-context HTTP header (roadmap A6 — traces).
const TraceparentHeader = "traceparent"

// This package ships a small, dependency-free W3C-style trace: a root span per
// HTTP request (created by the server middleware), with child spans for the
// agent loop and each inference batch. Spans are emitted as structured JSON log
// lines (event="span") carrying trace_id / span_id / parent_span_id so a
// request's full timeline can be reassembled by grepping trace_id. An OTel SDK
// can be swapped in later behind the same StartSpan/End shape.

type traceCtxKey struct{}
type remoteParentKey struct{}

// remoteParent is a trace context parsed from an inbound W3C traceparent header:
// the trace belongs to the caller, and our root span is its child.
type remoteParent struct{ traceID, spanID string }

// Span is one unit of work in a trace tree.
type Span struct {
	TraceID  string
	SpanID   string
	ParentID string
	Name     string

	start  time.Time
	attrs  []any
	logger *slog.Logger
}

// StartSpan starts a span under the current span (or remote parent) carried in
// ctx, returning a ctx that holds the new span. attrs is a key/value slice
// emitted on End (e.g. "batch_size", 4). Never log prompts, keys or tokens here.
func StartSpan(ctx context.Context, name string, attrs ...any) (context.Context, *Span) {
	traceID, parentID := "", ""
	if rp, ok := ctx.Value(remoteParentKey{}).(remoteParent); ok {
		traceID, parentID = rp.traceID, rp.spanID
	}
	if p, ok := ctx.Value(traceCtxKey{}).(*Span); ok {
		traceID, parentID = p.TraceID, p.SpanID
	}
	if traceID == "" {
		traceID = newID(16) // 16 bytes → 32 hex chars (W3C trace id)
	}
	s := &Span{
		TraceID:  traceID,
		SpanID:   newID(8), // 8 bytes → 16 hex chars (W3C span id)
		ParentID: parentID,
		Name:     name,
		start:    time.Now(),
		attrs:    attrs,
		logger:   slog.Default(),
	}
	return context.WithValue(ctx, traceCtxKey{}, s), s
}

// End emits the span as a structured JSON log line with its duration.
func (s *Span) End() {
	args := append([]any{
		"event", "span",
		"trace_id", s.TraceID,
		"span_id", s.SpanID,
		"parent_span_id", s.ParentID,
		"name", s.Name,
		"duration_ms", float64(time.Since(s.start).Microseconds()) / 1000.0,
	}, s.attrs...)
	s.logger.Info("span", args...)
}

// ExtractTraceparent seeds ctx with a remote parent parsed from a W3C
// traceparent header, so the next StartSpan continues the caller's trace.
// An absent or malformed header leaves ctx unchanged.
//
//	Format: version-traceid-spanid-flags, e.g. "00-<32 hex>-<16 hex>-01".
func ExtractTraceparent(ctx context.Context, header string) context.Context {
	parts := strings.Split(header, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return ctx
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 {
		return ctx
	}
	return context.WithValue(ctx, remoteParentKey{}, remoteParent{traceID: parts[1], spanID: parts[2]})
}

// newID returns n random bytes hex-encoded. crypto/rand practically never
// fails; on failure it falls back to a stable zero id so the request still gets
// a well-formed (if degenerate) trace rather than panicking.
func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}
