package controlplane

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Repository interfaces. The service layer depends on these; only the
// implementations in this package know GORM.

type ModelRepository interface {
	CreateModel(ctx context.Context, m *Model) error
	ListModels(ctx context.Context) ([]Model, error)
	GetModel(ctx context.Context, id string) (*Model, error)
	CreateVersion(ctx context.Context, mv *ModelVersion) error
}

type TemplateRepository interface {
	Create(ctx context.Context, t *ServingTemplate) error
	List(ctx context.Context) ([]ServingTemplate, error)
	Get(ctx context.Context, id string) (*ServingTemplate, error)
	CreateVersion(ctx context.Context, tv *TemplateVersion) error
}

type DeploymentRepository interface {
	Create(ctx context.Context, d *Deployment) error
	Get(ctx context.Context, id string) (*Deployment, error)
	List(ctx context.Context, tenantID string) ([]Deployment, error)
	Resolve(ctx context.Context, tenantID, modelName string) (*Deployment, error)
	UpdateStatus(ctx context.Context, id, status string, updatedAt time.Time) error
	CreateRevision(ctx context.Context, r *DeploymentRevision) error
	ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error)
	SetWorkloadRef(ctx context.Context, id, ref string) error
	CreateEndpoint(ctx context.Context, e *Endpoint) error
}

type QuotaRepository interface {
	Upsert(ctx context.Context, q *Quota) error
	List(ctx context.Context, tenantID string) ([]Quota, error)
}

type UsageRepository interface {
	Record(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
	Summary(ctx context.Context, tenantID string, from, to time.Time) (UsageSummary, error)
	Daily(ctx context.Context, tenantID string, from, to time.Time) ([]UsageDailyPoint, error)
	ByModel(ctx context.Context, tenantID string, from, to time.Time) ([]UsageByModel, error)
}

type IdempotencyRepository interface {
	Save(ctx context.Context, tenantID, key, resourceType, resourceID string) error
	Resolve(ctx context.Context, tenantID, key, resourceType string) (string, error)
}

// Repositories bundles every repository the control plane service needs.
type Repositories struct {
	Models      ModelRepository
	Templates   TemplateRepository
	Deployments DeploymentRepository
	Quotas      QuotaRepository
	Usage       UsageRepository
	Idempotency IdempotencyRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{
		Models:      &modelRepo{db: db},
		Templates:   &templateRepo{db: db},
		Deployments: &deploymentRepo{db: db},
		Quotas:      &quotaRepo{db: db},
		Usage:       &usageRepo{db: db},
		Idempotency: &idempotencyRepo{db: db},
	}
}
