package controlplane

import "time"

// GORM row types. These mirror the SQL schema exactly (table + column names)
// and are deliberately separate from the domain/JSON structs; repositories map
// between them. JSONB columns use GORM's `serializer:json`.

// --- usage / quota ---

type quotaRow struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID   string    `gorm:"column:tenant_id;type:uuid"`
	QuotaType  string    `gorm:"column:quota_type"`
	LimitValue int64     `gorm:"column:limit_value"`
	Period     string    `gorm:"column:period"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (quotaRow) TableName() string { return "tenant_quotas" }

type usageRow struct {
	ID               int64     `gorm:"column:id;primaryKey"`
	TenantID         string    `gorm:"column:tenant_id;type:uuid"`
	Model            string    `gorm:"column:model"`
	PromptTokens     int       `gorm:"column:prompt_tokens"`
	CompletionTokens int       `gorm:"column:completion_tokens"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

func (usageRow) TableName() string { return "usage_events" }

type idempotencyRow struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID     string    `gorm:"column:tenant_id;type:uuid"`
	Key          string    `gorm:"column:key"`
	ResourceType string    `gorm:"column:resource_type"`
	ResourceID   string    `gorm:"column:resource_id;type:uuid"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (idempotencyRow) TableName() string { return "idempotency_keys" }

// --- serving catalog ---

type modelRow struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey"`
	Name        string    `gorm:"column:name"`
	Description string    `gorm:"column:description"`
	Task        string    `gorm:"column:task"`
	Framework   string    `gorm:"column:framework"`
	Status      string    `gorm:"column:status"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (modelRow) TableName() string { return "models" }

type modelVersionRow struct {
	ID           string         `gorm:"column:id;type:uuid;primaryKey"`
	ModelID      string         `gorm:"column:model_id;type:uuid"`
	Version      string         `gorm:"column:version"`
	ArtifactURI  string         `gorm:"column:artifact_uri"`
	MetadataJSON map[string]any `gorm:"column:metadata_json;serializer:json"`
	Status       string         `gorm:"column:status"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
}

func (modelVersionRow) TableName() string { return "model_versions" }

type templateRow struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey"`
	Name        string    `gorm:"column:name"`
	Description string    `gorm:"column:description"`
	Runtime     string    `gorm:"column:runtime"`
	Status      string    `gorm:"column:status"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (templateRow) TableName() string { return "serving_templates" }

type templateVersionRow struct {
	ID           string            `gorm:"column:id;type:uuid;primaryKey"`
	TemplateID   string            `gorm:"column:template_id;type:uuid"`
	Version      string            `gorm:"column:version"`
	Image        string            `gorm:"column:image"`
	Command      []string          `gorm:"column:command;serializer:json"`
	Environment  map[string]string `gorm:"column:environment;serializer:json"`
	ConfigSchema map[string]any    `gorm:"column:config_schema;serializer:json"`
	CreatedAt    time.Time         `gorm:"column:created_at"`
}

func (templateVersionRow) TableName() string { return "serving_template_versions" }

type deploymentRow struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID          string    `gorm:"column:tenant_id;type:uuid"`
	ModelVersionID    string    `gorm:"column:model_version_id;type:uuid"`
	TemplateVersionID string    `gorm:"column:template_version_id;type:uuid"`
	Name              string    `gorm:"column:name"`
	Region            string    `gorm:"column:region"`
	DesiredReplicas   int       `gorm:"column:desired_replicas"`
	Status            string    `gorm:"column:status"`
	WorkloadRef       string    `gorm:"column:workload_ref"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (deploymentRow) TableName() string { return "deployments" }

type revisionRow struct {
	ID           string         `gorm:"column:id;type:uuid;primaryKey"`
	DeploymentID string         `gorm:"column:deployment_id;type:uuid"`
	Revision     int            `gorm:"column:revision"`
	SpecJSON     map[string]any `gorm:"column:spec_json;serializer:json"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	CreatedBy    *string        `gorm:"column:created_by;type:uuid"`
}

func (revisionRow) TableName() string { return "deployment_revisions" }

type endpointRow struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey"`
	DeploymentID string    `gorm:"column:deployment_id;type:uuid"`
	Path         string    `gorm:"column:path"`
	Protocol     string    `gorm:"column:protocol"`
	Status       string    `gorm:"column:status"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (endpointRow) TableName() string { return "endpoints" }

// --- mappers (row → domain) ---

func toQuota(r quotaRow) Quota {
	return Quota{ID: r.ID, TenantID: r.TenantID, QuotaType: r.QuotaType, LimitValue: r.LimitValue, Period: r.Period}
}

func toModel(r modelRow) Model {
	return Model{
		ID: r.ID, Name: r.Name, Description: r.Description, Task: r.Task,
		Framework: r.Framework, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toModelVersion(r modelVersionRow) ModelVersion {
	return ModelVersion{
		ID: r.ID, ModelID: r.ModelID, Version: r.Version, ArtifactURI: r.ArtifactURI,
		Metadata: r.MetadataJSON, Status: r.Status, CreatedAt: r.CreatedAt,
	}
}

func toTemplate(r templateRow) ServingTemplate {
	return ServingTemplate{
		ID: r.ID, Name: r.Name, Description: r.Description, Runtime: r.Runtime,
		Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toTemplateVersion(r templateVersionRow) TemplateVersion {
	return TemplateVersion{
		ID: r.ID, TemplateID: r.TemplateID, Version: r.Version, Image: r.Image,
		Command: r.Command, Environment: r.Environment, ConfigSchema: r.ConfigSchema,
		CreatedAt: r.CreatedAt,
	}
}

func toDeployment(r deploymentRow) Deployment {
	return Deployment{
		ID: r.ID, TenantID: r.TenantID, ModelVersionID: r.ModelVersionID,
		TemplateVersionID: r.TemplateVersionID, Name: r.Name, Region: r.Region,
		DesiredReplicas: r.DesiredReplicas, Status: r.Status, WorkloadRef: r.WorkloadRef,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toRevision(r revisionRow) DeploymentRevision {
	createdBy := ""
	if r.CreatedBy != nil {
		createdBy = *r.CreatedBy
	}
	return DeploymentRevision{
		ID: r.ID, DeploymentID: r.DeploymentID, Revision: r.Revision,
		Spec: r.SpecJSON, CreatedAt: r.CreatedAt, CreatedBy: createdBy,
	}
}

func toEndpoint(r endpointRow) Endpoint {
	return Endpoint{
		ID: r.ID, DeploymentID: r.DeploymentID, Path: r.Path, Protocol: r.Protocol,
		Status: r.Status, CreatedAt: r.CreatedAt,
	}
}
