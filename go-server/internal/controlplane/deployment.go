package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Deployment struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id"`
	ModelVersionID    string    `json:"model_version_id"`
	TemplateVersionID string    `json:"template_version_id"`
	Name              string    `json:"name"`
	Region            string    `json:"region"`
	DesiredReplicas   int       `json:"desired_replicas"`
	Status            string    `json:"status"`
	WorkloadRef       string    `json:"workload_ref,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type DeploymentRevision struct {
	ID           string         `json:"id"`
	DeploymentID string         `json:"deployment_id"`
	Revision     int            `json:"revision"`
	Spec         map[string]any `json:"spec"`
	CreatedAt    time.Time      `json:"created_at"`
	CreatedBy    string         `json:"created_by"`
}

// Endpoint is the routable serving address created when a deployment reaches
// READY (spec §5 routing).
type Endpoint struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deployment_id"`
	Path         string    `json:"path"`
	Protocol     string    `json:"protocol"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) CreateDeployment(ctx context.Context, d Deployment) (*Deployment, error) {
	d.ID = uuid.NewString()
	d.Status = DeploymentPending
	if err := s.repos.Deployments.Create(ctx, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Service) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	return s.repos.Deployments.Get(ctx, id)
}

func (s *Service) ListDeployments(ctx context.Context, tenantID string) ([]Deployment, error) {
	return s.repos.Deployments.List(ctx, tenantID)
}

// ResolveDeployment trả deployment READY mới nhất của tenant serve model `modelName`.
// ErrNotFound nếu không có model/version/deployment READY khớp tenant.
func (s *Service) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*Deployment, error) {
	return s.repos.Deployments.Resolve(ctx, tenantID, modelName)
}

var ErrInvalidTransition = errors.New("invalid deployment state transition")

// TransitionDeployment moves a deployment to `to`, enforcing the state machine.
func (s *Service) TransitionDeployment(ctx context.Context, id, to string) (*Deployment, error) {
	d, err := s.repos.Deployments.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanTransition(d.Status, to) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, d.Status, to)
	}
	d.Status = to
	d.UpdatedAt = time.Now().UTC()
	if err := s.repos.Deployments.UpdateStatus(ctx, id, to, d.UpdatedAt); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*DeploymentRevision, error) {
	r := &DeploymentRevision{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Spec: spec, CreatedBy: createdBy,
	}
	if err := s.repos.Deployments.CreateRevision(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error) {
	return s.repos.Deployments.ListRevisions(ctx, deploymentID)
}

// SetWorkloadRef records the compute reference bound to a deployment by its
// ComputeProvider.
func (s *Service) SetWorkloadRef(ctx context.Context, id, ref string) error {
	return s.repos.Deployments.SetWorkloadRef(ctx, id, ref)
}

// CreateEndpoint records a routable endpoint for a READY deployment.
func (s *Service) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*Endpoint, error) {
	e := &Endpoint{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Path: path, Protocol: protocol, Status: "ACTIVE",
	}
	if err := s.repos.Deployments.CreateEndpoint(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// nullableUUID returns a *string (nil for empty) to satisfy a uuid column.
func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
