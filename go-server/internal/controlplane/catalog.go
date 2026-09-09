package controlplane

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// --- models ---

func (s *Service) CreateModel(ctx context.Context, m Model) (*Model, error) {
	m.ID = uuid.NewString()
	m.Status = "ACTIVE"
	err := s.db.QueryRow(ctx,
		`INSERT INTO models (id, name, description, task, framework, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at, updated_at`,
		m.ID, m.Name, m.Description, m.Task, m.Framework, m.Status).
		Scan(&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}
	return &m, nil
}

func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, description, task, framework, status, created_at, updated_at
		 FROM models ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.Task, &m.Framework,
			&m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) GetModel(ctx context.Context, id string) (*Model, error) {
	var m Model
	err := s.db.QueryRow(ctx,
		`SELECT id, name, description, task, framework, status, created_at, updated_at
		 FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.Description, &m.Task, &m.Framework,
			&m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, err)
	}
	return &m, nil
}

func (s *Service) CreateModelVersion(ctx context.Context, mv ModelVersion) (*ModelVersion, error) {
	mv.ID = uuid.NewString()
	mv.Status = "ACTIVE"
	if mv.Metadata == nil {
		mv.Metadata = map[string]any{}
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO model_versions (id, model_id, version, artifact_uri, metadata_json, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at`,
		mv.ID, mv.ModelID, mv.Version, mv.ArtifactURI, mv.Metadata, mv.Status).
		Scan(&mv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create model version: %w", err)
	}
	return &mv, nil
}

// --- serving templates ---

func (s *Service) CreateTemplate(ctx context.Context, t ServingTemplate) (*ServingTemplate, error) {
	t.ID = uuid.NewString()
	t.Status = "ACTIVE"
	err := s.db.QueryRow(ctx,
		`INSERT INTO serving_templates (id, name, description, runtime, status)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at, updated_at`,
		t.ID, t.Name, t.Description, t.Runtime, t.Status).
		Scan(&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	return &t, nil
}

func (s *Service) ListTemplates(ctx context.Context) ([]ServingTemplate, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, description, runtime, status, created_at, updated_at
		 FROM serving_templates ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()
	out := []ServingTemplate{}
	for rows.Next() {
		var t ServingTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Runtime,
			&t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) GetTemplate(ctx context.Context, id string) (*ServingTemplate, error) {
	var t ServingTemplate
	err := s.db.QueryRow(ctx,
		`SELECT id, name, description, runtime, status, created_at, updated_at
		 FROM serving_templates WHERE id = $1`, id).
		Scan(&t.ID, &t.Name, &t.Description, &t.Runtime,
			&t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get template %s: %w", id, err)
	}
	return &t, nil
}

func (s *Service) CreateTemplateVersion(ctx context.Context, tv TemplateVersion) (*TemplateVersion, error) {
	tv.ID = uuid.NewString()
	if tv.Command == nil {
		tv.Command = []string{}
	}
	if tv.Environment == nil {
		tv.Environment = map[string]string{}
	}
	if tv.ConfigSchema == nil {
		tv.ConfigSchema = map[string]any{}
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO serving_template_versions (id, template_id, version, image, command, environment, config_schema)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING created_at`,
		tv.ID, tv.TemplateID, tv.Version, tv.Image, tv.Command, tv.Environment, tv.ConfigSchema).
		Scan(&tv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create template version: %w", err)
	}
	return &tv, nil
}
