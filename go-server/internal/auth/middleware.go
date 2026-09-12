package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/gin-gonic/gin"
)

// Gin context keys for the authenticated principal.
const (
	ctxClaimsKey = "auth.claims"
	ctxAPIKeyKey = "auth.apikey"
)

// authenticate validates a Bearer JWT and stores the claims on the Gin context.
// It writes the 401 response and returns false on failure.
func authenticate(c *gin.Context, secret []byte) bool {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
		return false
	}
	claims, err := ParseToken(secret, strings.TrimPrefix(h, prefix))
	if err != nil {
		writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
		return false
	}
	c.Set(ctxClaimsKey, claims)
	return true
}

// RequireAuth validates a Bearer JWT and stores *Claims in the Gin context.
func RequireAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticate(c, secret) {
			c.Next()
		}
	}
}

// RequirePermission wraps RequireAuth and additionally checks the role's action.
func RequirePermission(secret []byte, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticate(c, secret) {
			return
		}
		claims, ok := ClaimsFromContext(c)
		if !ok || !RoleAllows(claims.Role, action) {
			writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "permission denied")
			return
		}
		c.Next()
	}
}

// ClaimsFromContext extracts the authenticated claims.
func ClaimsFromContext(c *gin.Context) (*Claims, bool) {
	v, ok := c.Get(ctxClaimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*Claims)
	return claims, ok
}

// APIKeyFromContext extracts the API key authenticated by InferenceAuth.
func APIKeyFromContext(c *gin.Context) (*controlplane.APIKey, bool) {
	v, ok := c.Get(ctxAPIKeyKey)
	if !ok {
		return nil, false
	}
	key, ok := v.(*controlplane.APIKey)
	return key, ok
}

// TenantIDFromContext trả tenant từ Claims (JWT) hoặc APIKey, bất kể đường auth nào.
func TenantIDFromContext(c *gin.Context) (string, bool) {
	if claims, ok := ClaimsFromContext(c); ok {
		return claims.TenantID, true
	}
	if key, ok := APIKeyFromContext(c); ok {
		return key.TenantID, true
	}
	return "", false
}

// InferenceAuth gates inference routes behind a Bearer JWT or a Bearer API key.
// A JWT stores claims (read via ClaimsFromContext); an API key is stored under
// the API-key key (read via APIKeyFromContext). Invalid → 401; inactive/expired
// key → 403. Never logs the raw token.
func InferenceAuth(secret []byte, svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) {
			writeAuthError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
			return
		}
		tok := strings.TrimPrefix(h, prefix)
		if claims, err := ParseToken(secret, tok); err == nil {
			c.Set(ctxClaimsKey, claims)
			c.Next()
			return
		}
		key, err := svc.AuthenticateAPIKey(c.Request.Context(), tok)
		if err == nil {
			c.Set(ctxAPIKeyKey, key)
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

func writeAuthError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": msg}})
}
