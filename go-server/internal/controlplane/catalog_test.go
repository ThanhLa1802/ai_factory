package controlplane

import (
	"encoding/json"
	"testing"
)

func TestModelRoundTrip(t *testing.T) {
	m := Model{ID: "m1", Name: "deepseek-v3", Task: "text-generation", Framework: "vllm"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Model
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Name != "deepseek-v3" || out.Framework != "vllm" {
		t.Errorf("out = %+v", out)
	}
}
