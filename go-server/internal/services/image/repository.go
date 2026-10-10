package image

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Repository interfaces. The service depends on these; only the implementations
// here know GORM.

type AssetRepository interface {
	Create(ctx context.Context, a *Asset) error
	Get(ctx context.Context, tenantID, id string) (*Asset, error)
	List(ctx context.Context, tenantID string, limit int) ([]Asset, error)
}

type JobRepository interface {
	Create(ctx context.Context, j *Job) error
	Get(ctx context.Context, tenantID, id string) (*Job, error)
	List(ctx context.Context, tenantID string, limit int) ([]Job, error)
	UpdateStatus(ctx context.Context, id, status, errMsg string) error
	UpdateProgress(ctx context.Context, id string, step int) error
	// Claim atomically transitions up to limit claimable jobs to running and
	// returns them. A job is claimable when it is queued, or running with an
	// updated_at before staleBefore (a lease that lapsed, e.g. after a crash).
	Claim(ctx context.Context, limit int, staleBefore time.Time) ([]Job, error)
	// Finish moves a running job to a terminal status. The transition is guarded
	// on status=running, so a duplicate finish (or a job another replica already
	// finished) is a no-op.
	Finish(ctx context.Context, id, status, errMsg string) error
}

// Repositories bundles every repository the image service needs.
type Repositories struct {
	Assets AssetRepository
	Jobs   JobRepository
}

// NewRepositories builds the GORM-backed repository bundle.
func NewRepositories(db *gorm.DB) Repositories {
	return Repositories{Assets: &assetRepo{db: db}, Jobs: &jobRepo{db: db}}
}

// --- assets ---

type assetRepo struct{ db *gorm.DB }

func (r *assetRepo) Create(ctx context.Context, a *Asset) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	row := assetRow{
		ID: a.ID, TenantID: a.TenantID, JobID: nullableUUID(a.JobID), Model: a.Model,
		Prompt: a.Prompt, NegativePrompt: a.NegativePrompt, Width: a.Width,
		Height: a.Height, Steps: a.Steps, GuidanceScale: a.GuidanceScale,
		Seed: a.Seed, Scheduler: a.Scheduler, Path: a.Path, CreatedAt: a.CreatedAt,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create image asset: %w", err)
	}
	return nil
}

func (r *assetRepo) Get(ctx context.Context, tenantID, id string) (*Asset, error) {
	var row assetRow
	err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get image asset: %w", err)
	}
	a := toAsset(row)
	return &a, nil
}

func (r *assetRepo) List(ctx context.Context, tenantID string, limit int) ([]Asset, error) {
	var rows []assetRow
	q := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).
		Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list image assets: %w", err)
	}
	out := make([]Asset, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAsset(row))
	}
	return out, nil
}

// --- jobs ---

type jobRepo struct{ db *gorm.DB }

func (r *jobRepo) Create(ctx context.Context, j *Job) error {
	now := time.Now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = now
	}
	row := jobRow{
		ID: j.ID, TenantID: j.TenantID, Model: j.Model, Status: j.Status,
		Prompt: j.Prompt, NegativePrompt: j.NegativePrompt, Width: j.Width,
		Height: j.Height, Steps: j.Steps, GuidanceScale: j.GuidanceScale,
		Seed: j.Seed, NumImages: j.NumImages, Scheduler: j.Scheduler,
		ProgressStep: j.ProgressStep, Error: j.Error,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create image job: %w", err)
	}
	return nil
}

func (r *jobRepo) Get(ctx context.Context, tenantID, id string) (*Job, error) {
	var row jobRow
	err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get image job: %w", err)
	}
	j := toJob(row)
	return &j, nil
}

func (r *jobRepo) List(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	var rows []jobRow
	q := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).
		Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list image jobs: %w", err)
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, toJob(row))
	}
	return out, nil
}

func (r *jobRepo) UpdateStatus(ctx context.Context, id, status, errMsg string) error {
	err := r.db.WithContext(ctx).Model(&jobRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status": status, "error": errMsg, "updated_at": time.Now().UTC(),
		}).Error
	if err != nil {
		return fmt.Errorf("update image job status: %w", err)
	}
	return nil
}

func (r *jobRepo) UpdateProgress(ctx context.Context, id string, step int) error {
	err := r.db.WithContext(ctx).Model(&jobRow{}).
		Where("id = ? AND status = ?", id, JobRunning).
		Updates(map[string]any{"progress_step": step, "updated_at": time.Now().UTC()}).Error
	if err != nil {
		return fmt.Errorf("update image job progress: %w", err)
	}
	return nil
}

// Claim uses a CTE with FOR UPDATE SKIP LOCKED so concurrent API replicas never
// grab the same job. Re-claiming a stale running job resumes it after a crash;
// its progress_step is preserved so work continues where it stopped.
func (r *jobRepo) Claim(ctx context.Context, limit int, staleBefore time.Time) ([]Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []jobRow
	err := r.db.WithContext(ctx).Raw(`
		WITH claimed AS (
			SELECT id FROM image_jobs
			WHERE status = ? OR (status = ? AND updated_at < ?)
			ORDER BY created_at
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
		UPDATE image_jobs j
		SET status = ?, updated_at = now()
		FROM claimed c
		WHERE j.id = c.id
		RETURNING j.id, j.tenant_id, j.model, j.status, j.prompt, j.negative_prompt,
		          j.width, j.height, j.steps, j.guidance_scale, j.seed, j.num_images,
		          j.scheduler, j.progress_step, j.error, j.created_at, j.updated_at`,
		JobQueued, JobRunning, staleBefore, limit, JobRunning,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("claim image jobs: %w", err)
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, toJob(row))
	}
	return out, nil
}

func (r *jobRepo) Finish(ctx context.Context, id, status, errMsg string) error {
	err := r.db.WithContext(ctx).Model(&jobRow{}).
		Where("id = ? AND status = ?", id, JobRunning).
		Updates(map[string]any{
			"status": status, "error": errMsg, "updated_at": time.Now().UTC(),
		}).Error
	if err != nil {
		return fmt.Errorf("finish image job: %w", err)
	}
	return nil
}

func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
