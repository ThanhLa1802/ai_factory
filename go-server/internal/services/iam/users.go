package iam

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// --- tenants ---

func (s *Service) CreateTenant(ctx context.Context, name string) (*Tenant, error) {
	t := &Tenant{ID: uuid.NewString(), Name: name, Status: "ACTIVE"}
	if err := s.repos.Tenants.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) ListTenants(ctx context.Context) ([]Tenant, error) {
	return s.repos.Tenants.List(ctx)
}

// --- users + memberships ---

func (s *Service) CreateUser(ctx context.Context, username, email, passwordHash, role, tenantID string) (*User, error) {
	u := &User{ID: uuid.NewString(), Username: username, Email: email, Role: role, TenantID: tenantID, Status: "ACTIVE"}
	if err := s.repos.Users.Create(ctx, u, passwordHash); err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByUsername returns the user and its password hash for verification.
func (s *Service) GetUserByUsername(ctx context.Context, username string) (*User, string, error) {
	return s.repos.Users.GetByUsername(ctx, username)
}

// --- api keys ---

func (s *Service) CreateAPIKey(ctx context.Context, tenantID, name, keyHash string, expiresAt *time.Time) (*APIKey, error) {
	k := &APIKey{ID: uuid.NewString(), TenantID: tenantID, Name: name, Status: "ACTIVE", ExpiresAt: expiresAt}
	if err := s.repos.APIKeys.Create(ctx, k, keyHash); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Service) GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	return s.repos.APIKeys.GetByHash(ctx, keyHash)
}

// ListAPIKeys trả các API key của một tenant, mới nhất trước.
func (s *Service) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
	return s.repos.APIKeys.ListByTenant(ctx, tenantID)
}

// DeleteAPIKey xoá key theo id + tenant (scoping an toàn). Trả về key_hash đã
// xoá để caller invalidate cache-aside. ErrNotFound nếu không khớp.
func (s *Service) DeleteAPIKey(ctx context.Context, id, tenantID string) (string, error) {
	return s.repos.APIKeys.Delete(ctx, id, tenantID)
}
