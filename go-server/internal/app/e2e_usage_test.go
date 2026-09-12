package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-factory/go-server/internal/services/iam"
	"github.com/ai-factory/go-server/internal/services/usage"
	"github.com/google/uuid"
)

// TestUsageEndpointE2E exercises GET /api/v1/usage against a real Postgres.
func TestUsageEndpointE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	ts := newTestServices(t, d)

	tenant, _ := ts.iam.CreateTenant(ctx, "usage-e2e-"+uuid.NewString()[:8])
	hash, _ := iam.HashPassword("admin-pass")
	user, _ := ts.iam.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, iam.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _ = d.Gorm().Exec( `DELETE FROM users WHERE id = $1`, user.ID) })

	if err := ts.usage.RecordUsage(ctx, tenant.ID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := ts.usage.RecordUsage(ctx, tenant.ID, "qwen-3b", 20, 10); err != nil {
		t.Fatalf("record: %v", err)
	}

	mux := newTestEngine()
	ts.mountIAM(mux)
	ts.mountControlPlane(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Today   usage.UsageSummary      `json:"today"`
		Month   usage.UsageSummary      `json:"month"`
		Daily   []usage.UsageDailyPoint `json:"daily"`
		ByModel []usage.UsageByModel    `json:"by_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Today.TotalTokens != 45 || resp.Today.Requests != 2 {
		t.Fatalf("today = %+v, want total=45 requests=2", resp.Today)
	}
	if resp.Month.TotalTokens != 45 {
		t.Fatalf("month total = %d, want 45", resp.Month.TotalTokens)
	}
	if len(resp.ByModel) != 1 || resp.ByModel[0].Model != "qwen-3b" {
		t.Fatalf("by_model = %+v, want [qwen-3b]", resp.ByModel)
	}

	// No auth → 401.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth code = %d, want 401", rec.Code)
	}
}
