package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type idempotencyRepo struct{ db *gorm.DB }

// Save records key → resource. ON CONFLICT is a no-op so a concurrent duplicate
// create does not error.
func (r *idempotencyRepo) Save(ctx context.Context, tenantID, key, resourceType, resourceID string) error {
	row := idempotencyRow{
		ID: uuid.NewString(), TenantID: tenantID, Key: key,
		ResourceType: resourceType, ResourceID: resourceID,
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// Resolve returns the resource ID previously recorded for (tenant, key,
// resourceType), or ErrNotFound when the key is unknown.
func (r *idempotencyRepo) Resolve(ctx context.Context, tenantID, key, resourceType string) (string, error) {
	var row idempotencyRow
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND key = ? AND resource_type = ?", tenantID, key, resourceType).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve idempotency key: %w", err)
	}
	return row.ResourceID, nil
}
