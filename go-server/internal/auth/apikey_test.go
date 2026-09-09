package auth

import "testing"

func TestGenerateAPIKey(t *testing.T) {
	raw, hash := GenerateAPIKey()
	if len(raw) < 20 {
		t.Fatalf("raw key too short: %q", raw)
	}
	if HashAPIKey(raw) != hash {
		t.Error("HashAPIKey(raw) != returned hash")
	}
	if HashAPIKey("different") == hash {
		t.Error("hash collision with different input")
	}
}
