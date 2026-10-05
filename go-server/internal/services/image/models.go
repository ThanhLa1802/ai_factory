package image

import "time"

// GORM row types. These mirror the SQL schema (table + column names) and are
// deliberately separate from the domain structs; repositories map between them.

type assetRow struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;type:uuid"`
	JobID          *string   `gorm:"column:job_id;type:uuid"`
	Model          string    `gorm:"column:model"`
	Prompt         string    `gorm:"column:prompt"`
	NegativePrompt string    `gorm:"column:negative_prompt"`
	Width          int       `gorm:"column:width"`
	Height         int       `gorm:"column:height"`
	Steps          int       `gorm:"column:steps"`
	GuidanceScale  float64   `gorm:"column:guidance_scale"`
	Seed           int64     `gorm:"column:seed"`
	Scheduler      string    `gorm:"column:scheduler"`
	Path           string    `gorm:"column:path"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (assetRow) TableName() string { return "image_assets" }

type jobRow struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;type:uuid"`
	Model          string    `gorm:"column:model"`
	Status         string    `gorm:"column:status"`
	Prompt         string    `gorm:"column:prompt"`
	NegativePrompt string    `gorm:"column:negative_prompt"`
	Width          int       `gorm:"column:width"`
	Height         int       `gorm:"column:height"`
	Steps          int       `gorm:"column:steps"`
	GuidanceScale  float64   `gorm:"column:guidance_scale"`
	Seed           int64     `gorm:"column:seed"`
	NumImages      int       `gorm:"column:num_images"`
	Scheduler      string    `gorm:"column:scheduler"`
	ProgressStep   int       `gorm:"column:progress_step"`
	Error          string    `gorm:"column:error"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (jobRow) TableName() string { return "image_jobs" }

func toAsset(r assetRow) Asset {
	jobID := ""
	if r.JobID != nil {
		jobID = *r.JobID
	}
	return Asset{
		ID: r.ID, TenantID: r.TenantID, JobID: jobID, Model: r.Model,
		Prompt: r.Prompt, NegativePrompt: r.NegativePrompt, Width: r.Width,
		Height: r.Height, Steps: r.Steps, GuidanceScale: r.GuidanceScale,
		Seed: r.Seed, Scheduler: r.Scheduler, Path: r.Path, CreatedAt: r.CreatedAt,
	}
}

func toJob(r jobRow) Job {
	return Job{
		ID: r.ID, TenantID: r.TenantID, Model: r.Model, Status: r.Status,
		Prompt: r.Prompt, NegativePrompt: r.NegativePrompt, Width: r.Width,
		Height: r.Height, Steps: r.Steps, GuidanceScale: r.GuidanceScale,
		Seed: r.Seed, NumImages: r.NumImages, Scheduler: r.Scheduler,
		ProgressStep: r.ProgressStep, Error: r.Error,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
