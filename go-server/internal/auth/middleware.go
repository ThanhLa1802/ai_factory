package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// RequireAuth validates a Bearer JWT and stores *Claims in the context.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
				return
			}
			claims, err := ParseToken(secret, strings.TrimPrefix(h, prefix))
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
		})
	}
}

// RequirePermission wraps RequireAuth and additionally checks the role's action.
func RequirePermission(secret []byte, action string) func(http.Handler) http.Handler {
	requireAuth := RequireAuth(secret)
	return func(next http.Handler) http.Handler {
		return requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok || !RoleAllows(claims.Role, action) {
				writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "permission denied")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// ClaimsFromContext extracts the authenticated claims.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + msg + `"}}`))
}
