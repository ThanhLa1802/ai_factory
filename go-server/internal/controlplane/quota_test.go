package controlplane

import (
	"encoding/json"
	"testing"
)

func TestQuotaJSON(t *testing.T) {
	q := Quota{TenantID: "t1", QuotaType: "tokens", LimitValue: 50_000_000, Period: "month"}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Quota
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.QuotaType != "tokens" || out.LimitValue != 50_000_000 || out.Period != "month" {
		t.Errorf("out = %+v", out)
	}
}
