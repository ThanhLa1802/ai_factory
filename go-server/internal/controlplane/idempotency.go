package controlplane

import "context"

// Idempotency keys let a client retry a create (e.g. POST /deployments) with an
// Idempotency-Key header and get back the same resource instead of a duplicate
// (roadmap A5 — idempotency). Keys are scoped per tenant.

// SaveIdempotencyKey records key → resource.
func (s *Service) SaveIdempotencyKey(ctx context.Context, tenantID, key, resourceType, resourceID string) error {
	return s.repos.Idempotency.Save(ctx, tenantID, key, resourceType, resourceID)
}

// ResolveIdempotencyKey returns the resource ID previously recorded for
// (tenant, key, resourceType), or ErrNotFound when the key is unknown.
func (s *Service) ResolveIdempotencyKey(ctx context.Context, tenantID, key, resourceType string) (string, error) {
	return s.repos.Idempotency.Resolve(ctx, tenantID, key, resourceType)
}
