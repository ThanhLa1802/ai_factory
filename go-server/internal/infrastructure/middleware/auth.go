package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Gin context key for the authenticated principal.
const principalKey = "auth.principal"

// Errors returned by an Authenticator implementation.
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrKeyInactive  = errors.New("API key inactive")
)

// Principal is the authenticated caller. An API-key caller has an empty Role.
type Principal struct {
	TenantID string
	UserID   string
	Role     string
}

// Authenticator verifies credentials and authorizes actions. It is implemented
// by services/iam (D-P4-3); this package never imports a service.
type Authenticator interface {
	ParseToken(token string) (Principal, error)
	AuthenticateAPIKey(ctx context.Context, rawKey string) (Principal, error)
	Allows(role, action string) bool
}

// Action constants used by RequirePermission.
const (
	ActionTenantManage  = "tenant.manage"
	ActionTenantRead    = "tenant.read"
	ActionModelWrite    = "model.write"
	ActionModelRead     = "model.read"
	ActionTemplateWrite = "template.write"
	ActionTemplateRead  = "template.read"
	ActionDeployWrite   = "deployment.write"
	ActionDeployRead    = "deployment.read"
	ActionKeyManage     = "key.manage"
	ActionQuotaManage   = "quota.manage"
	ActionUsageRead     = "usage.read"
	ActionBillingRead   = "billing.read"
	ActionBillingManage = "billing.manage"
)

// RequireAuth validates a Bearer JWT and stores the principal on the context.
func RequireAuth(auth Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticate(c, auth) {
			c.Next()
		}
	}
}

// RequirePermission wraps RequireAuth and additionally checks the role's action.
func RequirePermission(auth Authenticator, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticate(c, auth) {
			return
		}
		p, _ := PrincipalFromContext(c)
		if !auth.Allows(p.Role, action) {
			writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "permission denied")
			return
		}
		c.Next()
	}
}

// InferenceAuth gates inference routes behind a Bearer JWT or a Bearer API key.
// A valid JWT is preferred; otherwise the token is treated as an API key.
// Invalid → 401; inactive/expired key → 403. Never logs the raw token.
func InferenceAuth(auth Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) {
			writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
			return
		}
		tok := strings.TrimPrefix(h, prefix)
		if p, err := auth.ParseToken(tok); err == nil {
			c.Set(principalKey, p)
			c.Next()
			return
		}
		p, err := auth.AuthenticateAPIKey(c.Request.Context(), tok)
		if err == nil {
			c.Set(principalKey, p)
			c.Next()
			return
		}
		if errors.Is(err, ErrKeyInactive) {
			writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "API key inactive or expired")
			return
		}
		writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
	}
}

// authenticate validates a Bearer JWT, stores the principal, and writes the 401
// response and returns false on failure.
func authenticate(c *gin.Context, auth Authenticator) bool {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
		return false
	}
	p, err := auth.ParseToken(strings.TrimPrefix(h, prefix))
	if err != nil {
		writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
		return false
	}
	c.Set(principalKey, p)
	return true
}

// PrincipalFromContext extracts the authenticated principal.
func PrincipalFromContext(c *gin.Context) (Principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}

func writeAuthError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": msg}})
}
