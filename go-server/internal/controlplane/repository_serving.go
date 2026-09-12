package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// --- models + versions ---

type modelRepo struct{ db *gorm.DB }

func (r *modelRepo) CreateModel(ctx context.Context, m *Model) error {
	row := modelRow{
		ID: m.ID, Name: m.Name, Description: m.Description,
		Task: m.Task, Framework: m.Framework, Status: m.Status,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create model: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

func (r *modelRepo) ListModels(ctx context.Context) ([]Model, error) {
	var rows []modelRow
	if err := r.db.WithContext(ctx).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	out := make([]Model, 0, len(rows))
	for _, row := range rows {
		out = append(out, toModel(row))
	}
	return out, nil
}

func (r *modelRepo) GetModel(ctx context.Context, id string) (*Model, error) {
	var row modelRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("get model %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, err)
	}
	m := toModel(row)
	return &m, nil
}

func (r *modelRepo) CreateVersion(ctx context.Context, mv *ModelVersion) error {
	row := modelVersionRow{
		ID: mv.ID, ModelID: mv.ModelID, Version: mv.Version,
		ArtifactURI: mv.ArtifactURI, MetadataJSON: mv.Metadata, Status: mv.Status,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create model version: %w", err)
	}
	mv.CreatedAt = row.CreatedAt
	return nil
}

// --- templates + versions ---

type templateRepo struct{ db *gorm.DB }

func (r *templateRepo) Create(ctx context.Context, t *ServingTemplate) error {
	row := templateRow{
		ID: t.ID, Name: t.Name, Description: t.Description,
		Runtime: t.Runtime, Status: t.Status,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	t.CreatedAt, t.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

func (r *templateRepo) List(ctx context.Context) ([]ServingTemplate, error) {
	var rows []templateRow
	if err := r.db.WithContext(ctx).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	out := make([]ServingTemplate, 0, len(rows))
	for _, row := range rows {
		out = append(out, toTemplate(row))
	}
	return out, nil
}

func (r *templateRepo) Get(ctx context.Context, id string) (*ServingTemplate, error) {
	var row templateRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("get template %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get template %s: %w", id, err)
	}
	t := toTemplate(row)
	return &t, nil
}

func (r *templateRepo) CreateVersion(ctx context.Context, tv *TemplateVersion) error {
	row := templateVersionRow{
		ID: tv.ID, TemplateID: tv.TemplateID, Version: tv.Version, Image: tv.Image,
		Command: tv.Command, Environment: tv.Environment, ConfigSchema: tv.ConfigSchema,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create template version: %w", err)
	}
	tv.CreatedAt = row.CreatedAt
	return nil
}

// --- deployments ---

type deploymentRepo struct{ db *gorm.DB }

func (r *deploymentRepo) Create(ctx context.Context, d *Deployment) error {
	row := deploymentRow{
		ID: d.ID, TenantID: d.TenantID, ModelVersionID: d.ModelVersionID,
		TemplateVersionID: d.TemplateVersionID, Name: d.Name, Region: d.Region,
		DesiredReplicas: d.DesiredReplicas, Status: d.Status,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create deployment: %w", err)
	}
	d.CreatedAt, d.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

func (r *deploymentRepo) Get(ctx context.Context, id string) (*Deployment, error) {
	var row deploymentRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("get deployment %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", id, err)
	}
	d := toDeployment(row)
	return &d, nil
}

func (r *deploymentRepo) List(ctx context.Context, tenantID string) ([]Deployment, error) {
	var rows []deploymentRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	out := make([]Deployment, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDeployment(row))
	}
	return out, nil
}

func (r *deploymentRepo) Resolve(ctx context.Context, tenantID, modelName string) (*Deployment, error) {
	var row deploymentRow
	err := r.db.WithContext(ctx).
		Table("deployments AS d").
		Select("d.*").
		Joins("JOIN model_versions mv ON mv.id = d.model_version_id").
		Joins("JOIN models m ON m.id = mv.model_id").
		Where("m.name = ? AND d.tenant_id = ? AND d.status = ?", modelName, tenantID, DeploymentReady).
		Order("d.created_at DESC").
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve deployment: %w", err)
	}
	d := toDeployment(row)
	return &d, nil
}

func (r *deploymentRepo) UpdateStatus(ctx context.Context, id, status string, updatedAt time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&deploymentRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "updated_at": updatedAt})
	if res.Error != nil {
		return fmt.Errorf("update deployment: %w", res.Error)
	}
	return nil
}

func (r *deploymentRepo) CreateRevision(ctx context.Context, rev *DeploymentRevision) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxRev int
		if err := tx.Model(&revisionRow{}).
			Where("deployment_id = ?", rev.DeploymentID).
			Select("COALESCE(MAX(revision), 0)").
			Scan(&maxRev).Error; err != nil {
			return fmt.Errorf("next revision: %w", err)
		}
		row := revisionRow{
			ID: rev.ID, DeploymentID: rev.DeploymentID, Revision: maxRev + 1,
			SpecJSON: rev.Spec, CreatedBy: nullableUUID(rev.CreatedBy),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("insert revision: %w", err)
		}
		rev.Revision = row.Revision
		rev.CreatedAt = row.CreatedAt
		return nil
	})
}

func (r *deploymentRepo) ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error) {
	var rows []revisionRow
	if err := r.db.WithContext(ctx).
		Where("deployment_id = ?", deploymentID).
		Order("revision").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	out := make([]DeploymentRevision, 0, len(rows))
	for _, row := range rows {
		out = append(out, toRevision(row))
	}
	return out, nil
}

func (r *deploymentRepo) SetWorkloadRef(ctx context.Context, id, ref string) error {
	res := r.db.WithContext(ctx).
		Model(&deploymentRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"workload_ref": ref, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return fmt.Errorf("set workload ref: %w", res.Error)
	}
	return nil
}

func (r *deploymentRepo) CreateEndpoint(ctx context.Context, e *Endpoint) error {
	row := endpointRow{
		ID: e.ID, DeploymentID: e.DeploymentID, Path: e.Path,
		Protocol: e.Protocol, Status: e.Status,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create endpoint: %w", err)
	}
	e.CreatedAt = row.CreatedAt
	return nil
}
