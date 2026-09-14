package iam

import "time"

// GORM row types. These mirror the SQL schema exactly (table + column names) and
// are deliberately separate from the domain/JSON structs; repositories map
// between them.

type tenantRow struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey"`
	Name      string    `gorm:"column:name"`
	Status    string    `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (tenantRow) TableName() string { return "tenants" }

type userRow struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey"`
	Username     string    `gorm:"column:username"`
	Email        string    `gorm:"column:email"`
	PasswordHash string    `gorm:"column:password_hash"`
	Status       string    `gorm:"column:status"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (userRow) TableName() string { return "users" }

type membershipRow struct {
	UserID    string    `gorm:"column:user_id;type:uuid;primaryKey"`
	TenantID  string    `gorm:"column:tenant_id;type:uuid;primaryKey"`
	Role      string    `gorm:"column:role"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (membershipRow) TableName() string { return "tenant_memberships" }

type apiKeyRow struct {
	ID         string     `gorm:"column:id;type:uuid;primaryKey"`
	TenantID   string     `gorm:"column:tenant_id;type:uuid"`
	Name       string     `gorm:"column:name"`
	KeyHash    string     `gorm:"column:key_hash"`
	Status     string     `gorm:"column:status"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
}

func (apiKeyRow) TableName() string { return "api_keys" }

// --- mappers (row → domain) ---

func toTenant(r tenantRow) Tenant {
	return Tenant{ID: r.ID, Name: r.Name, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func toAPIKey(r apiKeyRow) APIKey {
	return APIKey{ID: r.ID, TenantID: r.TenantID, Name: r.Name, Status: r.Status, ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt}
}
