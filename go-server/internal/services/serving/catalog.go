package serving

import (
	"context"

	"github.com/google/uuid"
)

// --- models ---

func (s *Service) CreateModel(ctx context.Context, m Model) (*Model, error) {
	m.ID = uuid.NewString()
	m.Status = "ACTIVE"
	if err := s.repos.Models.CreateModel(ctx, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	return s.repos.Models.ListModels(ctx)
}

func (s *Service) GetModel(ctx context.Context, id string) (*Model, error) {
	return s.repos.Models.GetModel(ctx, id)
}

func (s *Service) CreateModelVersion(ctx context.Context, mv ModelVersion) (*ModelVersion, error) {
	mv.ID = uuid.NewString()
	mv.Status = "ACTIVE"
	if mv.Metadata == nil {
		mv.Metadata = map[string]any{}
	}
	if err := s.repos.Models.CreateVersion(ctx, &mv); err != nil {
		return nil, err
	}
	return &mv, nil
}

// GetModelVersion returns the version of a model, or ErrNotFound.
func (s *Service) GetModelVersion(ctx context.Context, modelID, version string) (*ModelVersion, error) {
	return s.repos.Models.GetVersion(ctx, modelID, version)
}

// --- serving templates ---

func (s *Service) CreateTemplate(ctx context.Context, t ServingTemplate) (*ServingTemplate, error) {
	t.ID = uuid.NewString()
	t.Status = "ACTIVE"
	if err := s.repos.Templates.Create(ctx, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Service) ListTemplates(ctx context.Context) ([]ServingTemplate, error) {
	return s.repos.Templates.List(ctx)
}

func (s *Service) GetTemplate(ctx context.Context, id string) (*ServingTemplate, error) {
	return s.repos.Templates.Get(ctx, id)
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
	if err := s.repos.Templates.CreateVersion(ctx, &tv); err != nil {
		return nil, err
	}
	return &tv, nil
}

// GetTemplateVersion returns the version of a template, or ErrNotFound.
func (s *Service) GetTemplateVersion(ctx context.Context, templateID, version string) (*TemplateVersion, error) {
	return s.repos.Templates.GetVersion(ctx, templateID, version)
}
