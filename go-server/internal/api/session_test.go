package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/google/uuid"
)

// TestSessionEndpointsE2E exercises the JWT-authed session CRUD endpoints.
func TestSessionEndpointsE2E(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t)
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "sess-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	mgr := session.NewManagerWithStore(session.NewPGStore(d.Pool()))
	h := &Handler{sessionMgr: mgr, secret: secret, authSvc: authSvc, uiDir: t.TempDir()}
	cph := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	cph.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")
	authH := func() string { return "Bearer " + token }

	// Seed one session with a user message.
	sid := session.NewSessionID()
	sess, err := mgr.GetOrCreate(ctx, sid, tenant.ID, user.ID)
	if err != nil {
		t.Fatalf("get or create: %v", err)
	}
	sess.AddMessage(ctx, session.Message{Role: session.RoleUser, Content: "hello world"})

	// List contains the seeded session.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions", nil, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list []session.SessionSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	var found bool
	for _, s := range list {
		if s.ID == sid {
			found = true
		}
	}
	if !found {
		t.Fatalf("list = %+v, want contain %s", list, sid)
	}

	// Get returns messages.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("get code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages []session.Message `json:"messages"`
		Title    string            `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("get messages = %d, want 1", len(got.Messages))
	}

	// Rename.
	body, _ := json.Marshal(map[string]string{"title": "Renamed"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodPatch, "/api/v1/sessions/"+sid, body, authH()))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Get reflects the new title.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Title != "Renamed" {
		t.Fatalf("title = %q, want Renamed", got.Title)
	}

	// Delete → 204, then get → 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodDelete, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete code = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(http.MethodGet, "/api/v1/sessions/"+sid, nil, authH()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete code = %d, want 404", rec.Code)
	}

	// Missing auth → 401.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth list code = %d, want 401", rec.Code)
	}
}

func authedRequest(method, path string, body []byte, authHeader string) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", authHeader)
	return r
}
