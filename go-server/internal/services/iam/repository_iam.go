package iam

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// --- tenants ---

type tenantRepo struct{ db *gorm.DB }

func (r *tenantRepo) Create(ctx context.Context, t *Tenant) error {
	row := tenantRow{ID: t.ID, Name: t.Name, Status: t.Status}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create tenant: %w", err)
	}
	t.CreatedAt, t.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

func (r *tenantRepo) List(ctx context.Context) ([]Tenant, error) {
	var rows []tenantRow
	if err := r.db.WithContext(ctx).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	out := make([]Tenant, 0, len(rows))
	for _, row := range rows {
		out = append(out, toTenant(row))
	}
	return out, nil
}

// --- users ---

type userRepo struct{ db *gorm.DB }

// userAccount is the joined projection of users + tenant_memberships.
type userAccount struct {
	ID           string `gorm:"column:id"`
	Username     string `gorm:"column:username"`
	Email        string `gorm:"column:email"`
	PasswordHash string `gorm:"column:password_hash"`
	Status       string `gorm:"column:status"`
	Role         string `gorm:"column:role"`
	TenantID     string `gorm:"column:tenant_id"`
}

func (r *userRepo) Create(ctx context.Context, u *User, passwordHash string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user := userRow{ID: u.ID, Username: u.Username, Email: u.Email, PasswordHash: passwordHash, Status: u.Status}
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		membership := membershipRow{UserID: u.ID, TenantID: u.TenantID, Role: u.Role}
		if err := tx.Create(&membership).Error; err != nil {
			return fmt.Errorf("insert membership: %w", err)
		}
		return nil
	})
}

func (r *userRepo) GetByUsername(ctx context.Context, username string) (*User, string, error) {
	var acc userAccount
	err := r.db.WithContext(ctx).
		Table("users AS u").
		Select("u.id, u.username, u.email, u.password_hash, u.status, m.role, m.tenant_id").
		Joins("JOIN tenant_memberships m ON m.user_id = u.id").
		Where("u.username = ?", username).
		Take(&acc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", fmt.Errorf("get user: %w", ErrNotFound)
	}
	if err != nil {
		return nil, "", fmt.Errorf("get user: %w", err)
	}
	u := &User{
		ID: acc.ID, Username: acc.Username, Email: acc.Email,
		Role: acc.Role, TenantID: acc.TenantID, Status: acc.Status,
	}
	return u, acc.PasswordHash, nil
}

// --- api keys ---

type apiKeyRepo struct{ db *gorm.DB }

func (r *apiKeyRepo) Create(ctx context.Context, k *APIKey, keyHash string) error {
	row := apiKeyRow{
		ID: k.ID, TenantID: k.TenantID, Name: k.Name,
		KeyHash: keyHash, Status: k.Status, ExpiresAt: k.ExpiresAt,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	k.CreatedAt = row.CreatedAt
	return nil
}

func (r *apiKeyRepo) GetByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	var row apiKeyRow
	err := r.db.WithContext(ctx).Where("key_hash = ?", keyHash).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("get api key: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	key := toAPIKey(row)
	return &key, nil
}

func (r *apiKeyRepo) ListByTenant(ctx context.Context, tenantID string) ([]APIKey, error) {
	var rows []apiKeyRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIKey(row))
	}
	return out, nil
}

// Delete removes the key and returns its hash so the caller can invalidate the
// cache-aside entry. ErrNotFound when no matching (id, tenant) row exists.
func (r *apiKeyRepo) Delete(ctx context.Context, id, tenantID string) (string, error) {
	var hash string
	row := r.db.WithContext(ctx).Raw(
		`DELETE FROM api_keys WHERE id = ? AND tenant_id = ? RETURNING key_hash`, id, tenantID,
	).Row()
	if err := row.Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("delete api key: %w", err)
	}
	return hash, nil
}
