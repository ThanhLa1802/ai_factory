package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ai-factory/go-server/internal/circuitbreaker"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/retry"
)

// DeploymentStore is the slice of the control plane the worker needs. It is
// satisfied by *controlplane.Service (all methods exist after Task 3).
type DeploymentStore interface {
	GetDeployment(ctx context.Context, id string) (*controlplane.Deployment, error)
	TransitionDeployment(ctx context.Context, id, to string) (*controlplane.Deployment, error)
	CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*controlplane.DeploymentRevision, error)
	SetWorkloadRef(ctx context.Context, id, ref string) error
	CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*controlplane.Endpoint, error)
}

// Worker consumes deployment events and drives the deployment state machine
// asynchronously: deployment_created -> PENDING..READY, deployment_stop_requested
// -> STOPPED. Idempotent by construction: events whose deployment is already
// in flight/ready/stopped/failed are no-ops (the state machine guard).
type Worker struct {
	store    DeploymentStore
	adapter  ServingRuntimeAdapter
	compute  ComputeProvider
	producer events.Producer
	consumer events.Consumer
	log      *slog.Logger
	// cb trips open after repeated runtime failures so a known-down worker or
	// compute provider is not hammered with retries on every deployment (A5).
	cb *circuitbreaker.CircuitBreaker
}

func NewWorker(store DeploymentStore, adapter ServingRuntimeAdapter, compute ComputeProvider, producer events.Producer, consumer events.Consumer, log *slog.Logger) *Worker {
	return &Worker{
		store: store, adapter: adapter, compute: compute, producer: producer, consumer: consumer, log: log,
		cb: circuitbreaker.New(3, 30*time.Second),
	}
}

// retryAttempts / retryInitialDelay bound the in-process retry of transient
// provisioning steps (roadmap A5). Small and fast: a permanent failure still
// resolves to FAILED within ~150ms, while a transient runtime blip (worker or
// compute temporarily unreachable) is absorbed without failing the deployment.
const (
	retryAttempts     = 3
	retryInitialDelay = 50 * time.Millisecond
)

// runProvision runs fn with exponential backoff + jitter so a transient runtime
// blip (worker or compute temporarily unreachable) is retried before the caller
// decides to fail the deployment. It does not fail the deployment itself.
func (w *Worker) runProvision(ctx context.Context, fn func() error) error {
	return retry.Do(ctx, retry.Options{Attempts: retryAttempts, InitialDelay: retryInitialDelay}, fn)
}

// cbProbe runs fn (with retry) through the circuit breaker. While the breaker
// is open it returns circuitbreaker.ErrOpen without invoking fn, so a known-down
// runtime fails fast instead of burning retries on every new deployment.
func (w *Worker) cbProbe(ctx context.Context, fn func() error) error {
	return w.cb.Call(ctx, func() error { return w.runProvision(ctx, fn) })
}

// Run subscribes the worker to the deployment topic. Subscribe is non-blocking
// (handlers run on the bus's goroutine), so Run returns the subscribe error or
// nil immediately.
func (w *Worker) Run(ctx context.Context) error {
	return w.consumer.Subscribe(ctx, events.TopicDeploymentEvents, w.handle)
}

// handle routes deployment-topic events; unknown types are ignored.
func (w *Worker) handle(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.TypeDeploymentCreated:
		return w.onCreated(ctx, ev)
	case events.TypeDeploymentStopRequested:
		return w.onStop(ctx, ev)
	default:
		return nil
	}
}

func (w *Worker) onCreated(ctx context.Context, ev events.Event) error {
	d, err := w.store.GetDeployment(ctx, ev.ResourceID)
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", ev.ResourceID, err)
	}

	// Idempotency guard: terminal states are done; STOPPING means a stop is in
	// flight and wins. Any other state (PENDING/PROVISIONING/STARTING/STOPPED/
	// DEGRADED) resumes the state machine from where it is — a crash or at-least-
	// once redelivery may leave the deployment mid-provisioning, and resuming is
	// what makes the redelivery safe.
	switch d.Status {
	case controlplane.DeploymentReady, controlplane.DeploymentFailed, controlplane.DeploymentStopping:
		w.log.Info("deployment already handled", "id", d.ID, "status", d.Status)
		return nil
	}

	createdBy, _ := ev.Payload["created_by"].(string)
	ref := d.WorkloadRef

	if d.Status == controlplane.DeploymentStopped {
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentPending); err != nil {
			return fmt.Errorf("restart to pending: %w", err)
		}
		d.Status = controlplane.DeploymentPending
	}

	if d.Status == controlplane.DeploymentPending {
		if _, err := w.store.CreateRevision(ctx, d.ID, specMap(d), createdBy); err != nil {
			return fmt.Errorf("create revision: %w", err)
		}
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentProvisioning); err != nil {
			return w.fail(ctx, d, err)
		}
		d.Status = controlplane.DeploymentProvisioning
	}

	if d.Status == controlplane.DeploymentProvisioning {
		if err := w.cbProbe(ctx, func() error {
			var e error
			ref, e = w.compute.RequestCapacity(ctx, d)
			return e
		}); err != nil {
			return w.fail(ctx, d, fmt.Errorf("request capacity: %w", err))
		}
		if err := w.store.SetWorkloadRef(ctx, d.ID, ref); err != nil {
			return w.fail(ctx, d, fmt.Errorf("set workload ref: %w", err))
		}
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStarting); err != nil {
			return w.fail(ctx, d, err)
		}
		d.Status = controlplane.DeploymentStarting
	}

	if d.Status == controlplane.DeploymentStarting {
		if err := w.cbProbe(ctx, func() error {
			return w.adapter.Start(ctx, d)
		}); err != nil {
			return w.fail(ctx, d, fmt.Errorf("adapter start: %w", err))
		}
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentReady); err != nil {
			return w.fail(ctx, d, err)
		}
	}

	if d.Status == controlplane.DeploymentDegraded {
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentReady); err != nil {
			return w.fail(ctx, d, err)
		}
	}

	if _, err := w.store.CreateEndpoint(ctx, d.ID, endpointPath(d.ID), "openai"); err != nil {
		w.log.Warn("endpoint not created", "deployment", d.ID, "err", err)
	}

	w.log.Info("deployment ready", "id", d.ID, "workload_ref", ref)
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentReady, d.TenantID, d.ID, map[string]any{"workload_ref": ref}))
}

func (w *Worker) onStop(ctx context.Context, ev events.Event) error {
	d, err := w.store.GetDeployment(ctx, ev.ResourceID)
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", ev.ResourceID, err)
	}
	if d.Status == controlplane.DeploymentStopped || d.Status == controlplane.DeploymentStopping {
		w.log.Info("deployment already stopped/stopping", "id", d.ID, "status", d.Status)
		return nil
	}
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStopping); err != nil {
		// A deployment that is still PENDING/PROVISIONING/STARTING/FAILED cannot
		// transition to STOPPING yet (state.go). That is expected, not fatal:
		// treat it as a no-op rather than killing the consumer.
		if errors.Is(err, controlplane.ErrInvalidTransition) {
			w.log.Info("deployment not stoppable yet; ignoring stop", "id", d.ID, "status", d.Status)
			return nil
		}
		return fmt.Errorf("transition to stopping: %w", err)
	}
	if err := w.adapter.Stop(ctx, d); err != nil {
		return w.fail(ctx, d, fmt.Errorf("adapter stop: %w", err))
	}
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStopped); err != nil {
		return w.fail(ctx, d, err)
	}
	if err := w.compute.ReleaseCapacity(ctx, d); err != nil {
		w.log.Warn("release capacity", "deployment", d.ID, "err", err)
	}
	w.log.Info("deployment stopped", "id", d.ID)
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentStopped, d.TenantID, d.ID, nil))
}

// fail transitions to FAILED (terminal), releases any acquired compute capacity
// (best-effort), and publishes deployment_failed.
func (w *Worker) fail(ctx context.Context, d *controlplane.Deployment, cause error) error {
	w.log.Error("deployment failed", "id", d.ID, "cause", cause)
	if err := w.compute.ReleaseCapacity(ctx, d); err != nil {
		w.log.Warn("release capacity on failure", "deployment", d.ID, "err", err)
	}
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentFailed); err != nil &&
		!errors.Is(err, controlplane.ErrInvalidTransition) {
		return fmt.Errorf("mark failed: %w", err)
	}
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentFailed, d.TenantID, d.ID, map[string]any{"error": cause.Error()}))
}

// specMap captures the deployment spec for a revision record.
func specMap(d *controlplane.Deployment) map[string]any {
	return map[string]any{
		"name":                d.Name,
		"region":              d.Region,
		"desired_replicas":    d.DesiredReplicas,
		"model_version_id":    d.ModelVersionID,
		"template_version_id": d.TemplateVersionID,
	}
}

// endpointPath is the placeholder routable path for a READY deployment
// (spec §5 routing; a real inference gateway resolves this in M3).
func endpointPath(deploymentID string) string {
	return "/v1/chat/completions/" + deploymentID
}
