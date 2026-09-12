package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/gin-gonic/gin"
)

func newRouter(handlers ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/", handlers...)
	e.POST("/v1/chat/completions", handlers...)
	return e
}

func do(e *gin.Engine, method, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRequireAuthAcceptsValidToken(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)

	e := newRouter(RequireAuth(secret), func(c *gin.Context) {
		claims, ok := ClaimsFromContext(c)
		if !ok {
			c.String(http.StatusInternalServerError, "no claims")
			return
		}
		c.String(http.StatusOK, claims.TenantID)
	})

	rec := do(e, http.MethodGet, "/", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "t1" {
		t.Errorf("body = %q, want t1", rec.Body.String())
	}
}

func TestRequireAuthRejectsMissing(t *testing.T) {
	secret := []byte("0123456789abcdef")
	e := newRouter(RequireAuth(secret), func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := do(e, http.MethodGet, "/", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestRequirePermissionDeniesViewerWrite(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)
	e := newRouter(RequirePermission(secret, ActionDeployWrite), func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := do(e, http.MethodGet, "/", "Bearer "+token)
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
	e := newRouter(InferenceAuth(secret, svc), func(c *gin.Context) {
		claims, ok := ClaimsFromContext(c)
		if !ok {
			t.Error("ClaimsFromContext: not ok")
		}
		gotTenant = claims.TenantID
	})
	rec := do(e, http.MethodPost, "/v1/chat/completions", "Bearer "+token)
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
	var gotTenant string
	e := newRouter(InferenceAuth(secret, svc), func(c *gin.Context) {
		key, ok := APIKeyFromContext(c)
		if !ok {
			t.Error("APIKeyFromContext: not ok")
		}
		gotTenant = key.TenantID
	})
	rec := do(e, http.MethodPost, "/v1/chat/completions", "Bearer sk-whatever")
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
	e := newRouter(InferenceAuth(secret, svc), func(c *gin.Context) { t.Error("next must not run without auth") })
	rec := do(e, http.MethodPost, "/v1/chat/completions", "")
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
	e := newRouter(InferenceAuth(secret, svc), func(c *gin.Context) { t.Error("next must not run on invalid token") })
	rec := do(e, http.MethodPost, "/v1/chat/completions", "Bearer not-a-token-not-a-key")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestInferenceAuthInactiveKey(t *testing.T) {
	secret := []byte("0123456789abcdef")
	svc := NewService(&fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t9", Status: "REVOKED"}}, secret, time.Hour)
	e := newRouter(InferenceAuth(secret, svc), func(c *gin.Context) { t.Error("next must not run on inactive key") })
	rec := do(e, http.MethodPost, "/v1/chat/completions", "Bearer sk-inactive")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("body = %s, want FORBIDDEN error json", rec.Body.String())
	}
}

func TestTenantIDFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if _, ok := TenantIDFromContext(c); ok {
		t.Fatal("empty context → want ok=false")
	}

	c.Set(ctxClaimsKey, &Claims{UserID: "u1", TenantID: "tenant-1", Role: RoleTenantAdmin})
	if id, ok := TenantIDFromContext(c); !ok || id != "tenant-1" {
		t.Fatalf("claims → (%q,%v), want (tenant-1,true)", id, ok)
	}

	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Set(ctxAPIKeyKey, &controlplane.APIKey{ID: "k1", TenantID: "tenant-2"})
	if id, ok := TenantIDFromContext(c2); !ok || id != "tenant-2" {
		t.Fatalf("api key → (%q,%v), want (tenant-2,true)", id, ok)
	}
}
