package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/google/uuid"
)

// TestUsageEndpointE2E exercises GET /api/v1/usage against a real Postgres.
func TestUsageEndpointE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "usage-e2e-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	if err := cp.RecordUsage(ctx, tenant.ID, "qwen-3b", 10, 5); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := cp.RecordUsage(ctx, tenant.ID, "qwen-3b", 20, 10); err != nil {
		t.Fatalf("record: %v", err)
	}

	cph := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())
	mux := http.NewServeMux()
	cph.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Today   controlplane.UsageSummary      `json:"today"`
		Month   controlplane.UsageSummary      `json:"month"`
		Daily   []controlplane.UsageDailyPoint `json:"daily"`
		ByModel []controlplane.UsageByModel    `json:"by_model"`
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
