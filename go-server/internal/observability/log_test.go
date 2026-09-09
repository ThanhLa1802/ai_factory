package observability

import (
	"testing"
)

func TestSetupLoggerJSON(t *testing.T) {
	logger := SetupLogger("debug")
	if logger == nil {
		t.Fatal("SetupLogger returned nil")
	}
}
