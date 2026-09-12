package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestStartSpanChildCarriesTrace(t *testing.T) {
	ctx, root := StartSpan(context.Background(), "http")
	childCtx, child := StartSpan(ctx, "agent.loop")
	_ = childCtx

	if child.TraceID != root.TraceID {
		t.Fatalf("child trace = %q, want parent trace %q", child.TraceID, root.TraceID)
	}
	if child.ParentID != root.SpanID {
		t.Fatalf("child parent = %q, want root span %q", child.ParentID, root.SpanID)
	}
	if child.SpanID == "" || child.SpanID == root.SpanID {
		t.Fatalf("child span id %q must be distinct and non-empty", child.SpanID)
	}
}

func TestStartSpanRootGeneratesTrace(t *testing.T) {
	_, span := StartSpan(context.Background(), "http")
	if len(span.TraceID) != 32 {
		t.Fatalf("trace id = %q, want 32 hex chars", span.TraceID)
	}
	if len(span.SpanID) != 16 {
		t.Fatalf("span id = %q, want 16 hex chars", span.SpanID)
	}
	if span.ParentID != "" {
		t.Fatalf("root parent = %q, want empty", span.ParentID)
	}
}

func TestExtractTraceparent(t *testing.T) {
	traceID := strings.Repeat("a", 32)
	spanID := strings.Repeat("b", 16)
	ctx := ExtractTraceparent(context.Background(), "00-"+traceID+"-"+spanID+"-01")
	_, span := StartSpan(ctx, "http")
	if span.TraceID != traceID {
		t.Fatalf("trace = %q, want %q", span.TraceID, traceID)
	}
	if span.ParentID != spanID {
		t.Fatalf("parent = %q, want %q", span.ParentID, spanID)
	}
}

func TestExtractTraceparentMalformedIgnored(t *testing.T) {
	for _, h := range []string{"", "garbage", "00-abc-xyz-01", "01-aa-bb-cc", "00-" + strings.Repeat("a", 33) + "-bb-01"} {
		ctx := ExtractTraceparent(context.Background(), h)
		_, span := StartSpan(ctx, "http")
		if span.ParentID != "" {
			t.Fatalf("header %q: parent = %q, want empty", h, span.ParentID)
		}
	}
}

func TestSpanEndEmitsStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	_, span := StartSpan(context.Background(), "inference.batch", "batch_size", 2)
	span.End()

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("span log not valid JSON: %v", err)
	}
	if m["event"] != "span" {
		t.Fatalf("event = %v, want span", m["event"])
	}
	if m["name"] != "inference.batch" {
		t.Fatalf("name = %v, want inference.batch", m["name"])
	}
	if m["batch_size"] != float64(2) {
		t.Fatalf("batch_size = %v, want 2", m["batch_size"])
	}
	if m["trace_id"] == "" || m["span_id"] == "" {
		t.Fatalf("trace_id/span_id missing: %v", m)
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Fatalf("duration_ms missing: %v", m)
	}
}
