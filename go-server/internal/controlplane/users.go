package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is the control plane use-case layer. All methods write to PostgreSQL.
type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// --- tenants ---

func (s *Service) CreateTenant(ctx context.Context, name string) (*Tenant, error) {
	t := &Tenant{ID: uuid.NewString(), Name: name, Status: "ACTIVE"}
	err := s.db.QueryRow(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2)
		 RETURNING status, created_at, updated_at`,
		t.ID, t.Name).Scan(&t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}

func (s *Service) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, status, created_at, updated_at FROM tenants ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	out := []Tenant{}
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- users + memberships ---

func (s *Service) CreateUser(ctx context.Context, username, email, passwordHash, role, tenantID string) (*User, error) {
	u := &User{ID: uuid.NewString(), Username: username, Email: email, Role: role, TenantID: tenantID, Status: "ACTIVE"}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1, $2, $3, $4)`,
		u.ID, u.Username, u.Email, passwordHash); err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant_memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)`,
		u.ID, tenantID, role); err != nil {
		return nil, fmt.Errorf("insert membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return u, nil
}

// GetUserByUsername returns the user and its password hash for verification.
func (s *Service) GetUserByUsername(ctx context.Context, username string) (*User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx,
		`SELECT u.id, u.username, u.email, u.status, m.role, m.tenant_id, u.password_hash
		 FROM users u
		 JOIN tenant_memberships m ON m.user_id = u.id
		 WHERE u.username = $1`, username).
		Scan(&u.ID, &u.Username, &u.Email, &u.Status, &u.Role, &u.TenantID, &hash)
	if err != nil {
		return nil, "", fmt.Errorf("get user: %w", err)
	}
	return &u, hash, nil
}

// --- api keys ---

func (s *Service) CreateAPIKey(ctx context.Context, tenantID, name, keyHash string, expiresAt *time.Time) (*APIKey, error) {
	k := &APIKey{ID: uuid.NewString(), TenantID: tenantID, Name: name, Status: "ACTIVE", ExpiresAt: expiresAt}
	err := s.db.QueryRow(ctx,
		`INSERT INTO api_keys (id, tenant_id, name, key_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at`,
		k.ID, k.TenantID, k.Name, keyHash, expiresAt).Scan(&k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return k, nil
}

func (s *Service) GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	var k APIKey
	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, name, status, expires_at, created_at
		 FROM api_keys WHERE key_hash = $1`, keyHash).
		Scan(&k.ID, &k.TenantID, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return &k, nil
}

var ErrNotFound = errors.New("not found")

// ListAPIKeys trả các API key của một tenant, mới nhất trước.
func (s *Service) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, name, status, expires_at, created_at
		 FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey xoá key theo id + tenant (scoping an toàn). ErrNotFound nếu không khớp.
func (s *Service) DeleteAPIKey(ctx context.Context, id, tenantID string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
