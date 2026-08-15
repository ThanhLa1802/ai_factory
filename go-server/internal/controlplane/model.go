package controlplane

import "time"

type Model struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Task        string    `json:"task"`
	Framework   string    `json:"framework"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ModelVersion struct {
	ID          string         `json:"id"`
	ModelID     string         `json:"model_id"`
	Version     string         `json:"version"`
	ArtifactURI string         `json:"artifact_uri"`
	Metadata    map[string]any `json:"metadata"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
}

type ServingTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Runtime     string    `json:"runtime"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TemplateVersion struct {
	ID           string            `json:"id"`
	TemplateID   string            `json:"template_id"`
	Version      string            `json:"version"`
	Image        string            `json:"image"`
	Command      []string          `json:"command"`
	Environment  map[string]string `json:"environment"`
	ConfigSchema map[string]any    `json:"config_schema"`
	CreatedAt    time.Time         `json:"created_at"`
}
