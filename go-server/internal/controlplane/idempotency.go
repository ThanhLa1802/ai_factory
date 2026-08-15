package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Idempotency keys let a client retry a create (e.g. POST /deployments) with an
// Idempotency-Key header and get back the same resource instead of a duplicate
// (roadmap A5 — idempotency). Keys are scoped per tenant.

// SaveIdempotencyKey records key → resource. ON CONFLICT is a no-op so a
// concurrent duplicate create does not error.
func (s *Service) SaveIdempotencyKey(ctx context.Context, tenantID, key, resourceType, resourceID string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key, resource_type, resource_id)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, key) DO NOTHING`,
		tenantID, key, resourceType, resourceID)
	if err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// ResolveIdempotencyKey returns the resource ID previously recorded for
// (tenant, key, resourceType), or ErrNotFound when the key is unknown.
func (s *Service) ResolveIdempotencyKey(ctx context.Context, tenantID, key, resourceType string) (string, error) {
	var resourceID string
	err := s.db.QueryRow(ctx,
		`SELECT resource_id FROM idempotency_keys WHERE tenant_id = $1 AND key = $2 AND resource_type = $3`,
		tenantID, key, resourceType).Scan(&resourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve idempotency key: %w", err)
	}
	return resourceID, nil
}
