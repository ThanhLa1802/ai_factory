package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestSetupLoggerJSON(t *testing.T) {
	logger := SetupLogger("debug")
	if logger == nil {
		t.Fatal("SetupLogger returned nil")
	}
}

func TestZapCoreEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	core := newZapCore(&buf, "debug")
	logger := slog.New(newSlogHandler(core))
	logger.Info("hello", "tenant", "t1")

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v (raw=%q)", err, buf.String())
	}
	if got["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", got["msg"])
	}
	if got["tenant"] != "t1" {
		t.Errorf("tenant = %v, want t1", got["tenant"])
	}
}

func TestZapCoreRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	core := newZapCore(&buf, "error")
	logger := slog.New(newSlogHandler(core))
	logger.Info("dropped")
	if strings.Contains(buf.String(), "dropped") {
		t.Fatalf("info logged at error level: %q", buf.String())
	}
}
