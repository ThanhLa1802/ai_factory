package iam

import (
	"context"

	"gorm.io/gorm"
)

// Repository interfaces. The service layer depends on these; only the
// implementations in this package know GORM.

type TenantRepository interface {
	Create(ctx context.Context, t *Tenant) error
	List(ctx context.Context) ([]Tenant, error)
}

type UserRepository interface {
	Create(ctx context.Context, u *User, passwordHash string) error
	GetByUsername(ctx context.Context, username string) (*User, string, error)
	ListByTenant(ctx context.Context, tenantID string) ([]User, error)
}

type APIKeyRepository interface {
	Create(ctx context.Context, k *APIKey, keyHash string) error
	GetByHash(ctx context.Context, keyHash string) (*APIKey, error)
	ListByTenant(ctx context.Context, tenantID string) ([]APIKey, error)
	Delete(ctx context.Context, id, tenantID string) (string, error)
}

// Repositories bundles every repository the IAM service needs.
type Repositories struct {
	Tenants TenantRepository
	Users   UserRepository
	APIKeys APIKeyRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{
		Tenants: &tenantRepo{db: db},
		Users:   &userRepo{db: db},
		APIKeys: &apiKeyRepo{db: db},
	}
}
