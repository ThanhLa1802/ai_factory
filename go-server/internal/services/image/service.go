package image

import (
	"context"
	"errors"
	"fmt"
	"strings"

	infraimage "github.com/ai-factory/go-server/internal/infrastructure/image"
	"github.com/google/uuid"
)

// Generator is the service's view of the Python image worker. Satisfied by
// *infraimage.Client; a fake satisfies it in tests.
type Generator interface {
	Generate(ctx context.Context, req infraimage.GenerateRequest) (*infraimage.GenerateResult, error)
	Stream(ctx context.Context, req infraimage.GenerateRequest) (<-chan infraimage.ProgressEvent, error)
}

// Config holds generation defaults/caps from configuration.
type Config struct {
	DefaultWidth    int
	DefaultHeight   int
	DefaultSteps    int
	DefaultGuidance float64
	MaxImages       int
}

// Service is the image use-case layer over repository interfaces + the worker.
type Service struct {
	repos     Repositories
	generator Generator
	storage   Storage
	resolver  DeploymentResolver
	cfg       Config
}

// NewService builds the service.
func NewService(repos Repositories, generator Generator, storage Storage, resolver DeploymentResolver, cfg Config) *Service {
	return &Service{repos: repos, generator: generator, storage: storage, resolver: resolver, cfg: cfg}
}

const (
	minDimension = 256
	maxDimension = 1536
	sizeMultiple = 8
)

// Generate runs a synchronous text-to-image request and persists each image.
func (s *Service) Generate(ctx context.Context, tenantID string, p GenerateParams) (*Generation, error) {
	if err := s.normalize(&p); err != nil {
		return nil, err
	}
	if _, err := s.resolver.ResolveDeployment(ctx, tenantID, p.Model); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelNotFound, err)
	}

	result, err := s.generator.Generate(ctx, infraimage.GenerateRequest{
		RequestID:      uuid.NewString(),
		Prompt:         p.Prompt,
		NegativePrompt: p.NegativePrompt,
		Width:          int32(p.Width),
		Height:         int32(p.Height),
		Steps:          int32(p.Steps),
		GuidanceScale:  float32(p.GuidanceScale),
		Seed:           p.Seed,
		NumImages:      int32(p.NumImages),
		Scheduler:      p.Scheduler,
	})
	if err != nil {
		return nil, err
	}

	assets := make([]Asset, 0, len(result.PNG))
	for _, png := range result.PNG {
		id := uuid.NewString()
		path, err := s.storage.Save(id, png)
		if err != nil {
			return nil, err
		}
		a := Asset{
			ID: id, TenantID: tenantID, Model: p.Model, Prompt: p.Prompt,
			NegativePrompt: p.NegativePrompt, Width: int(result.Width),
			Height: int(result.Height), Steps: int(result.Steps),
			GuidanceScale: p.GuidanceScale, Seed: result.Seed,
			Scheduler: result.Scheduler, Path: path,
		}
		if err := s.repos.Assets.Create(ctx, &a); err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}

	return &Generation{
		Assets: assets, Seed: result.Seed, Width: int(result.Width),
		Height: int(result.Height), Steps: int(result.Steps), RevisedPrompt: p.Prompt,
	}, nil
}

// CreateJob validates and records a queued async job (executed by the runner).
func (s *Service) CreateJob(ctx context.Context, tenantID string, p GenerateParams) (*Job, error) {
	if err := s.normalize(&p); err != nil {
		return nil, err
	}
	if _, err := s.resolver.ResolveDeployment(ctx, tenantID, p.Model); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelNotFound, err)
	}
	j := &Job{
		ID: uuid.NewString(), TenantID: tenantID, Model: p.Model, Status: JobQueued,
		Prompt: p.Prompt, NegativePrompt: p.NegativePrompt, Width: p.Width,
		Height: p.Height, Steps: p.Steps, GuidanceScale: p.GuidanceScale,
		Seed: p.Seed, NumImages: p.NumImages, Scheduler: p.Scheduler,
	}
	if err := s.repos.Jobs.Create(ctx, j); err != nil {
		return nil, err
	}
	return j, nil
}

// ExecuteJob runs one claimed async job: it streams generation from the worker
// (persisting progress_step as it goes) and saves each produced PNG with the
// job id set. The caller is responsible for the job's running→terminal
// transition; ExecuteJob only does the work.
func (s *Service) ExecuteJob(ctx context.Context, job Job) error {
	if _, err := s.resolver.ResolveDeployment(ctx, job.TenantID, job.Model); err != nil {
		return fmt.Errorf("%w: %v", ErrModelNotFound, err)
	}

	events, err := s.generator.Stream(ctx, infraimage.GenerateRequest{
		RequestID:      job.ID,
		Prompt:         job.Prompt,
		NegativePrompt: job.NegativePrompt,
		Width:          int32(job.Width),
		Height:         int32(job.Height),
		Steps:          int32(job.Steps),
		GuidanceScale:  float32(job.GuidanceScale),
		Seed:           job.Seed,
		NumImages:      int32(job.NumImages),
		Scheduler:      job.Scheduler,
	})
	if err != nil {
		return err
	}

	var result *infraimage.GenerateResult
	for ev := range events {
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		if ev.Step > 0 {
			_ = s.repos.Jobs.UpdateProgress(ctx, job.ID, int(ev.Step))
		}
		if ev.Done {
			result = ev.Result
			break
		}
	}
	if result == nil {
		return errors.New("image stream ended without a result")
	}

	for _, png := range result.PNG {
		id := uuid.NewString()
		path, err := s.storage.Save(id, png)
		if err != nil {
			return err
		}
		a := Asset{
			ID: id, TenantID: job.TenantID, JobID: job.ID, Model: job.Model,
			Prompt: job.Prompt, NegativePrompt: job.NegativePrompt,
			Width: int(result.Width), Height: int(result.Height), Steps: int(result.Steps),
			GuidanceScale: job.GuidanceScale, Seed: result.Seed,
			Scheduler: result.Scheduler, Path: path,
		}
		if err := s.repos.Assets.Create(ctx, &a); err != nil {
			return err
		}
	}
	return nil
}

// ListJobs returns the tenant's jobs, newest first.
func (s *Service) ListJobs(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	return s.repos.Jobs.List(ctx, tenantID, limit)
}

// GetJob returns a tenant-scoped job, or ErrNotFound.
func (s *Service) GetJob(ctx context.Context, tenantID, id string) (*Job, error) {
	return s.repos.Jobs.Get(ctx, tenantID, id)
}

// ListAssets returns the tenant's generated images, newest first.
func (s *Service) ListAssets(ctx context.Context, tenantID string, limit int) ([]Asset, error) {
	return s.repos.Assets.List(ctx, tenantID, limit)
}

// GetAsset returns an asset's metadata and PNG bytes, tenant-scoped.
func (s *Service) GetAsset(ctx context.Context, tenantID, id string) (*Asset, []byte, error) {
	a, err := s.repos.Assets.Get(ctx, tenantID, id)
	if err != nil {
		return nil, nil, err
	}
	data, err := s.storage.Load(a.Path)
	if err != nil {
		return nil, nil, err
	}
	return a, data, nil
}

// normalize applies defaults and validates a request in place.
func (s *Service) normalize(p *GenerateParams) error {
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return fmt.Errorf("%w: prompt is required", ErrInvalidRequest)
	}
	if p.Width <= 0 {
		p.Width = s.cfg.DefaultWidth
	}
	if p.Height <= 0 {
		p.Height = s.cfg.DefaultHeight
	}
	if err := validateSize(p.Width, p.Height); err != nil {
		return err
	}
	if p.Steps <= 0 {
		p.Steps = s.cfg.DefaultSteps
	}
	if p.GuidanceScale < 0 {
		p.GuidanceScale = s.cfg.DefaultGuidance
	}
	if p.NumImages <= 0 {
		p.NumImages = 1
	}
	maxN := s.cfg.MaxImages
	if maxN <= 0 {
		maxN = 1
	}
	if p.NumImages > maxN {
		return fmt.Errorf("%w: n exceeds max_images (%d)", ErrInvalidRequest, maxN)
	}
	return nil
}

func validateSize(w, h int) error {
	for _, d := range []int{w, h} {
		if d < minDimension || d > maxDimension {
			return fmt.Errorf("%w: width/height must be between %d and %d", ErrInvalidRequest, minDimension, maxDimension)
		}
		if d%sizeMultiple != 0 {
			return fmt.Errorf("%w: width/height must be a multiple of %d", ErrInvalidRequest, sizeMultiple)
		}
	}
	return nil
}
