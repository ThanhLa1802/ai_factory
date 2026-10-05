// Package image owns text-to-image generation: the OpenAI-compatible
// /v1/images/generations endpoint, persisted image assets, and async jobs. It
// talks to the Python image worker over gRPC (a separate process/port from the
// text worker; spec D3).
package image

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalidRequest is returned when the request is malformed (bad size, n, ...).
var ErrInvalidRequest = errors.New("invalid request")

// ErrModelNotFound is returned when no READY deployment serves the model.
var ErrModelNotFound = errors.New("no READY deployment for model")

// ResolvedDeployment is this service's view of a routable deployment (defined
// here so image never imports services/serving).
type ResolvedDeployment struct {
	ID       string
	TenantID string
	Region   string
}

// DeploymentResolver resolves model name → READY deployment. Satisfied by an
// adapter over *serving.Service, wired in the composition root.
type DeploymentResolver interface {
	ResolveDeployment(ctx context.Context, tenantID, modelName string) (*ResolvedDeployment, error)
}

// Asset is a persisted generated image (metadata; PNG lives on disk).
type Asset struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	JobID          string    `json:"job_id,omitempty"`
	Model          string    `json:"model"`
	Prompt         string    `json:"prompt"`
	NegativePrompt string    `json:"negative_prompt"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Steps          int       `json:"steps"`
	GuidanceScale  float64   `json:"guidance_scale"`
	Seed           int64     `json:"seed"`
	Scheduler      string    `json:"scheduler"`
	Path           string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

// Job status values.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job is an async image-generation job.
type Job struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	Model          string    `json:"model"`
	Status         string    `json:"status"`
	Prompt         string    `json:"prompt"`
	NegativePrompt string    `json:"negative_prompt"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Steps          int       `json:"steps"`
	GuidanceScale  float64   `json:"guidance_scale"`
	Seed           int64     `json:"seed"`
	NumImages      int       `json:"num_images"`
	Scheduler      string    `json:"scheduler"`
	ProgressStep   int       `json:"progress_step"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GenerateParams is a normalized generation request (defaults already applied).
type GenerateParams struct {
	Model          string
	Prompt         string
	NegativePrompt string
	Width          int
	Height         int
	Steps          int
	GuidanceScale  float64
	Seed           int64 // -1 => random
	NumImages      int
	Scheduler      string
}

// Generation is the result of a synchronous generation.
type Generation struct {
	Assets        []Asset
	Seed          int64
	Width         int
	Height        int
	Steps         int
	RevisedPrompt string
}
