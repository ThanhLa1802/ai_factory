package serving

import (
	"context"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/message"
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
	CreateWithEvent(ctx context.Context, d *Deployment, topic string, ev message.Event) error
	Get(ctx context.Context, id string) (*Deployment, error)
	List(ctx context.Context, tenantID string) ([]Deployment, error)
	Resolve(ctx context.Context, tenantID, modelName string) (*Deployment, error)
	UpdateStatus(ctx context.Context, id, status string, updatedAt time.Time) error
	CreateRevision(ctx context.Context, r *DeploymentRevision) error
	ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error)
	SetWorkloadRef(ctx context.Context, id, ref string) error
	CreateEndpoint(ctx context.Context, e *Endpoint) error
}

type IdempotencyRepository interface {
	Save(ctx context.Context, tenantID, key, resourceType, resourceID string) error
	Resolve(ctx context.Context, tenantID, key, resourceType string) (string, error)
}

// EventSink durably enqueues a domain event (transactional outbox). Implemented
// by outbox.Store; kept consumer-defined so serving never imports the outbox
// package.
type EventSink interface {
	Enqueue(ctx context.Context, topic string, ev message.Event) error
}

// Repositories bundles every repository the serving service needs.
type Repositories struct {
	Models      ModelRepository
	Templates   TemplateRepository
	Deployments DeploymentRepository
	Idempotency IdempotencyRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{
		Models:      &modelRepo{db: db},
		Templates:   &templateRepo{db: db},
		Deployments: &deploymentRepo{db: db},
		Idempotency: &idempotencyRepo{db: db},
	}
}
