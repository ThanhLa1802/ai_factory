package events

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Topics per spec §6.1. M2 publishes/consumes only the deployment topic.
const (
	TopicDeploymentEvents = "serving.deployment.events"
	TopicInferenceEvents  = "serving.inference.events"
	TopicAuditEvents      = "serving.audit.events"
)

// Event types on the deployment topic.
const (
	TypeDeploymentCreated       = "deployment_created"
	TypeDeploymentReady         = "deployment_ready"
	TypeDeploymentFailed        = "deployment_failed"
	TypeDeploymentStopRequested = "deployment_stop_requested"
	TypeDeploymentStopped       = "deployment_stopped"
)

// Event is the spec §6.2 envelope. JSON field names are part of the contract —
// do not rename without updating the spec and every consumer.
type Event struct {
	ID         string         `json:"event_id"`
	Type       string         `json:"event_type"`
	Version    int            `json:"event_version"`
	Timestamp  time.Time      `json:"timestamp"`
	TenantID   string         `json:"tenant_id"`
	ResourceID string         `json:"resource_id"`
	TraceID    string         `json:"trace_id"`
	Payload    map[string]any `json:"payload"`
}

// NewEvent builds an envelope with fresh UUIDs and a UTC timestamp.
func NewEvent(eventType, tenantID, resourceID string, payload map[string]any) Event {
	return Event{
		ID:         uuid.NewString(),
		Type:       eventType,
		Version:    1,
		Timestamp:  time.Now().UTC(),
		TenantID:   tenantID,
		ResourceID: resourceID,
		TraceID:    uuid.NewString(),
		Payload:    payload,
	}
}

// Producer publishes events to a topic.
type Producer interface {
	Publish(ctx context.Context, topic string, ev Event) error
	Close() error
}

// Consumer subscribes a handler to a topic. Subscribe must return promptly
// (handlers run on their own goroutines); Close releases resources.
type Consumer interface {
	Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, ev Event) error) error
	Close() error
}
