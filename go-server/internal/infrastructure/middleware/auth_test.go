package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakeAuth struct {
	parse  func(string) (Principal, error)
	apiKey func(context.Context, string) (Principal, error)
	allows func(role, action string) bool
}

func (f *fakeAuth) ParseToken(tok string) (Principal, error) {
	if f.parse == nil {
		return Principal{}, ErrInvalidToken
	}
	return f.parse(tok)
}

func (f *fakeAuth) AuthenticateAPIKey(ctx context.Context, raw string) (Principal, error) {
	if f.apiKey == nil {
		return Principal{}, errors.New("no api key")
	}
	return f.apiKey(ctx, raw)
}

func (f *fakeAuth) Allows(role, action string) bool {
	if f.allows == nil {
		return false
	}
	return f.allows(role, action)
}

// runMW runs one middleware over a bare handler and reports the status plus the
// principal the handler observed (nil if none).
func runMW(mw gin.HandlerFunc, authz string) (*httptest.ResponseRecorder, *Principal) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	var captured *Principal
	e.GET("/", mw, func(c *gin.Context) {
		if p, ok := PrincipalFromContext(c); ok {
			captured = &p
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, captured
}

func TestRequireAuth(t *testing.T) {
	valid := func(string) (Principal, error) {
		return Principal{TenantID: "t1", UserID: "u1", Role: "TENANT_ADMIN"}, nil
	}

	if rec, _ := runMW(RequireAuth(&fakeAuth{}), ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", rec.Code)
	}
	if rec, _ := runMW(RequireAuth(&fakeAuth{parse: func(string) (Principal, error) { return Principal{}, ErrInvalidToken }}), "Bearer bad"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token = %d, want 401", rec.Code)
	}
	rec, p := runMW(RequireAuth(&fakeAuth{parse: valid}), "Bearer good")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token = %d, want 200", rec.Code)
	}
	if p == nil || p.TenantID != "t1" || p.UserID != "u1" {
		t.Fatalf("principal = %+v, want t1/u1", p)
	}
}

func TestRequirePermission(t *testing.T) {
	auth := &fakeAuth{
		parse:  func(string) (Principal, error) { return Principal{Role: "TENANT_ADMIN"}, nil },
		allows: func(role, action string) bool { return role == "TENANT_ADMIN" && action == ActionKeyManage },
	}
	if rec, _ := runMW(RequirePermission(auth, ActionKeyManage), "Bearer x"); rec.Code != http.StatusOK {
		t.Fatalf("allowed = %d, want 200", rec.Code)
	}
	if rec, _ := runMW(RequirePermission(auth, ActionTenantManage), "Bearer x"); rec.Code != http.StatusForbidden {
		t.Fatalf("denied = %d, want 403", rec.Code)
	}
}

func TestInferenceAuth(t *testing.T) {
	jwtAuth := &fakeAuth{parse: func(string) (Principal, error) {
		return Principal{TenantID: "t-jwt"}, nil
	}}
	rec, p := runMW(InferenceAuth(jwtAuth), "Bearer jwt")
	if rec.Code != http.StatusOK || p == nil || p.TenantID != "t-jwt" {
		t.Fatalf("jwt: code=%d principal=%+v", rec.Code, p)
	}

	keyAuth := &fakeAuth{
		parse:  func(string) (Principal, error) { return Principal{}, ErrInvalidToken },
		apiKey: func(context.Context, string) (Principal, error) { return Principal{TenantID: "t-key"}, nil },
	}
	rec, p = runMW(InferenceAuth(keyAuth), "Bearer ak")
	if rec.Code != http.StatusOK || p == nil || p.TenantID != "t-key" {
		t.Fatalf("api key: code=%d principal=%+v", rec.Code, p)
	}

	inactive := &fakeAuth{
		parse:  func(string) (Principal, error) { return Principal{}, ErrInvalidToken },
		apiKey: func(context.Context, string) (Principal, error) { return Principal{}, ErrKeyInactive },
	}
	if rec, _ := runMW(InferenceAuth(inactive), "Bearer ak"); rec.Code != http.StatusForbidden {
		t.Fatalf("inactive key = %d, want 403", rec.Code)
	}

	invalid := &fakeAuth{
		parse:  func(string) (Principal, error) { return Principal{}, ErrInvalidToken },
		apiKey: func(context.Context, string) (Principal, error) { return Principal{}, errors.New("nope") },
	}
	if rec, _ := runMW(InferenceAuth(invalid), "Bearer ak"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid creds = %d, want 401", rec.Code)
	}

	if rec, _ := runMW(InferenceAuth(&fakeAuth{}), ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing = %d, want 401", rec.Code)
	}
}
