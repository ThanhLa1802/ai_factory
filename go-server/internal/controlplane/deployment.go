package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	err := s.db.QueryRow(ctx,
		`INSERT INTO deployments (id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.ModelVersionID, d.TemplateVersionID, d.Name, d.Region, d.DesiredReplicas, d.Status).
		Scan(&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	return &d, nil
}

func (s *Service) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, workload_ref, created_at, updated_at
		 FROM deployments WHERE id = $1`, id).
		Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.WorkloadRef, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", id, err)
	}
	return &d, nil
}

func (s *Service) ListDeployments(ctx context.Context, tenantID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, workload_ref, created_at, updated_at
		 FROM deployments WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.WorkloadRef, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ResolveDeployment trả deployment READY mới nhất của tenant serve model `modelName`.
// ErrNotFound nếu không có model/version/deployment READY khớp tenant.
func (s *Service) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx,
		`SELECT d.id, d.tenant_id, d.model_version_id, d.template_version_id, d.name,
		        d.region, d.desired_replicas, d.status, d.workload_ref, d.created_at, d.updated_at
		 FROM deployments d
		 JOIN model_versions mv ON mv.id = d.model_version_id
		 JOIN models m        ON m.id  = mv.model_id
		 WHERE m.name = $1 AND d.tenant_id = $2 AND d.status = $3
		 ORDER BY d.created_at DESC
		 LIMIT 1`,
		modelName, tenantID, DeploymentReady).
		Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.WorkloadRef, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve deployment: %w", err)
	}
	return &d, nil
}

var ErrInvalidTransition = errors.New("invalid deployment state transition")

// TransitionDeployment moves a deployment to `to`, enforcing the state machine.
func (s *Service) TransitionDeployment(ctx context.Context, id, to string) (*Deployment, error) {
	d, err := s.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanTransition(d.Status, to) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, d.Status, to)
	}
	d.Status = to
	d.UpdatedAt = time.Now().UTC()
	if _, err := s.db.Exec(ctx,
		`UPDATE deployments SET status = $1, updated_at = $2 WHERE id = $3`,
		d.Status, d.UpdatedAt, d.ID); err != nil {
		return nil, fmt.Errorf("update deployment: %w", err)
	}
	return d, nil
}

func (s *Service) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*DeploymentRevision, error) {
	var rev int
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision), 0) + 1 FROM deployment_revisions WHERE deployment_id = $1`,
		deploymentID).Scan(&rev); err != nil {
		return nil, fmt.Errorf("next revision: %w", err)
	}
	r := &DeploymentRevision{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Revision: rev, Spec: spec, CreatedBy: createdBy,
	}
	if err := s.db.QueryRow(ctx,
		`INSERT INTO deployment_revisions (id, deployment_id, revision, spec_json, created_by)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		r.ID, r.DeploymentID, r.Revision, spec, nullableUUID(createdBy)).Scan(&r.CreatedAt); err != nil {
		return nil, fmt.Errorf("insert revision: %w", err)
	}
	return r, nil
}

func (s *Service) ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, revision, spec_json, created_at, COALESCE(created_by::TEXT, '')
		 FROM deployment_revisions WHERE deployment_id = $1 ORDER BY revision`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer rows.Close()
	out := []DeploymentRevision{}
	for rows.Next() {
		var r DeploymentRevision
		if err := rows.Scan(&r.ID, &r.DeploymentID, &r.Revision, &r.Spec, &r.CreatedAt, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetWorkloadRef records the compute reference bound to a deployment by its
// ComputeProvider.
func (s *Service) SetWorkloadRef(ctx context.Context, id, ref string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE deployments SET workload_ref = $1, updated_at = now() WHERE id = $2`,
		ref, id)
	if err != nil {
		return fmt.Errorf("set workload ref: %w", err)
	}
	return nil
}

// CreateEndpoint records a routable endpoint for a READY deployment.
func (s *Service) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*Endpoint, error) {
	e := &Endpoint{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Path: path, Protocol: protocol, Status: "ACTIVE",
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO endpoints (id, deployment_id, path, protocol, status)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		e.ID, e.DeploymentID, e.Path, e.Protocol, e.Status).Scan(&e.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create endpoint: %w", err)
	}
	return e, nil
}

// nullableUUID returns a *string (nil for empty) to satisfy the uuid column.
func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
