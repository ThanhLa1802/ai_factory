package controlplane

import (
	"context"

	"github.com/google/uuid"
)

type Quota struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	QuotaType  string `json:"quota_type"`
	LimitValue int64  `json:"limit_value"`
	Period     string `json:"period"`
}

// UpsertQuota creates or updates a tenant quota (unique on tenant/type/period).
func (s *Service) UpsertQuota(ctx context.Context, q Quota) (*Quota, error) {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	if err := s.repos.Quotas.Upsert(ctx, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

func (s *Service) ListQuotas(ctx context.Context, tenantID string) ([]Quota, error) {
	return s.repos.Quotas.List(ctx, tenantID)
}
