package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

func TestRequireAuthAcceptsValidToken(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)

	handler := RequireAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "no claims", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(claims.TenantID))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "t1" {
		t.Errorf("body = %q, want t1", rec.Body.String())
	}
}

func TestRequireAuthRejectsMissing(t *testing.T) {
	secret := []byte("0123456789abcdef")
	handler := RequireAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestRequirePermissionDeniesViewerWrite(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)
	handler := RequirePermission(secret, ActionDeployWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestInferenceAuthJWT(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, err := IssueToken(secret, "u1", "t1", RoleTenantAdmin, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	svc := NewService(&fakeStore{}, secret, time.Hour)
	var gotTenant string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			t.Error("ClaimsFromContext: not ok")
		}
		gotTenant = claims.TenantID
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if gotTenant != "t1" {
		t.Errorf("tenant = %q, want t1", gotTenant)
	}
}

func TestInferenceAuthAPIKey(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t9", Status: "ACTIVE"}}, secret, time.Hour)
	// raw key bất kỳ: fakeStore trả key cố định, không cần hash khớp
	var gotTenant string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Error("APIKeyFromContext: not ok")
		}
		gotTenant = key.TenantID
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-whatever")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if gotTenant != "t9" {
		t.Errorf("tenant = %q, want t9", gotTenant)
	}
}

func TestInferenceAuthMissingHeader(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run without auth")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil) // no Authorization
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("body = %s, want UNAUTHORIZED error json", rec.Body.String())
	}
}

func TestInferenceAuthInvalidToken(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run on invalid token")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer not-a-token-not-a-key")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestInferenceAuthInactiveKey(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t9", Status: "REVOKED"}}, secret, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next must not run on inactive key")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-inactive")
	rec := httptest.NewRecorder()
	InferenceAuth(secret, svc)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("body = %s, want FORBIDDEN error json", rec.Body.String())
	}
}
