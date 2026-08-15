package events

import (
	"encoding/json"
	"testing"
)

// TestEventEnvelopeJSON asserts the wire format matches spec §6.2 field-for-field.
func TestEventEnvelopeJSON(t *testing.T) {
	ev := NewEvent(TypeDeploymentCreated, "tenant-1", "deploy-1", map[string]any{"name": "svc"})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"event_id", "event_type", "event_version", "timestamp", "tenant_id", "resource_id", "trace_id", "payload"} {
		if _, ok := got[k]; !ok {
			t.Errorf("envelope missing key %q (raw: %s)", k, raw)
		}
	}
	if got["event_type"] != TypeDeploymentCreated {
		t.Errorf("event_type = %v, want %s", got["event_type"], TypeDeploymentCreated)
	}
	if got["tenant_id"] != "tenant-1" || got["resource_id"] != "deploy-1" {
		t.Errorf("tenant/resource mismatch: %s", raw)
	}
}
