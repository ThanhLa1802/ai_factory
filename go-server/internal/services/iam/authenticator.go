package iam

import (
	"context"
	"errors"

	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
)

// Authenticator adapts IAM authentication to the neutral middleware port
// (D-P4-3). It is the only bridge between services/iam and the auth middleware.
type Authenticator struct {
	secret  []byte
	authSvc *AuthService
}

func NewAuthenticator(secret []byte, authSvc *AuthService) *Authenticator {
	return &Authenticator{secret: secret, authSvc: authSvc}
}

// ParseToken verifies a JWT and maps its claims onto a Principal.
func (a *Authenticator) ParseToken(token string) (middleware.Principal, error) {
	claims, err := ParseToken(a.secret, token)
	if err != nil {
		return middleware.Principal{}, middleware.ErrInvalidToken
	}
	return middleware.Principal{TenantID: claims.TenantID, UserID: claims.UserID, Role: claims.Role}, nil
}

// AuthenticateAPIKey verifies an API key and maps it onto a Principal (no role).
func (a *Authenticator) AuthenticateAPIKey(ctx context.Context, rawKey string) (middleware.Principal, error) {
	key, err := a.authSvc.AuthenticateAPIKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, ErrKeyInactive) {
			return middleware.Principal{}, middleware.ErrKeyInactive
		}
		return middleware.Principal{}, middleware.ErrInvalidToken
	}
	return middleware.Principal{TenantID: key.TenantID}, nil
}

// Allows reports whether a role may perform an action.
func (a *Authenticator) Allows(role, action string) bool { return RoleAllows(role, action) }
