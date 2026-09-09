package controlplane

import (
	"context"
	"fmt"

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
	err := s.db.QueryRow(ctx,
		`INSERT INTO tenant_quotas (id, tenant_id, quota_type, limit_value, period)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tenant_id, quota_type, period)
		 DO UPDATE SET limit_value = EXCLUDED.limit_value
		 RETURNING id`,
		q.ID, q.TenantID, q.QuotaType, q.LimitValue, q.Period).Scan(&q.ID)
	if err != nil {
		return nil, fmt.Errorf("upsert quota: %w", err)
	}
	return &q, nil
}

func (s *Service) ListQuotas(ctx context.Context, tenantID string) ([]Quota, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, quota_type, limit_value, period
		 FROM tenant_quotas WHERE tenant_id = $1 ORDER BY quota_type`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list quotas: %w", err)
	}
	defer rows.Close()
	out := []Quota{}
	for rows.Next() {
		var q Quota
		if err := rows.Scan(&q.ID, &q.TenantID, &q.QuotaType, &q.LimitValue, &q.Period); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
