package serving

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/circuitbreaker"
	"github.com/ai-factory/go-server/internal/infrastructure/message"
)

type fakeStore struct {
	mu          sync.Mutex
	deployments map[string]*Deployment
	workloadRef string
	endpoints   []*Endpoint
	revSpecs    []map[string]any
}

func newFakeStore() *fakeStore {
	return &fakeStore{deployments: map[string]*Deployment{}}
}

func (f *fakeStore) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	cp := *d
	return &cp, nil
}

func (f *fakeStore) TransitionDeployment(ctx context.Context, id, to string) (*Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	if !CanTransition(d.Status, to) {
		return nil, ErrInvalidTransition
	}
	d.Status = to
	cp := *d
	return &cp, nil
}

func (f *fakeStore) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*DeploymentRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revSpecs = append(f.revSpecs, spec)
	return &DeploymentRevision{
		ID: "rev-1", DeploymentID: deploymentID, Revision: 1,
		Spec: spec, CreatedBy: createdBy,
	}, nil
}

func (f *fakeStore) SetWorkloadRef(ctx context.Context, id, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workloadRef = ref
	return nil
}

func (f *fakeStore) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := &Endpoint{ID: "ep-1", DeploymentID: deploymentID, Path: path, Protocol: protocol, Status: "ACTIVE"}
	f.endpoints = append(f.endpoints, e)
	return e, nil
}

// failAdapter fails Start to exercise the FAILED path.
type failAdapter struct{ *WorkerAdapter }

func (a failAdapter) Start(ctx context.Context, d *Deployment) error {
	return errors.New("worker exploded")
}

func newTestWorker(store DeploymentStore, adapter ServingRuntimeAdapter, bus *message.MemoryEventBus) *Worker {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorker(store, adapter, NewMockComputeProvider(), bus, bus, log)
}

func TestWorkerOnCreatedHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := message.NewMemoryEventBus()
	var published []message.Event
	if err := bus.Subscribe(context.Background(), message.TopicDeploymentEvents,
		func(ctx context.Context, ev message.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", map[string]any{"created_by": "u1"})
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentReady {
		t.Fatalf("status = %s, want %s", got, DeploymentReady)
	}
	if store.workloadRef == "" {
		t.Fatal("workload_ref not set")
	}
	if len(store.endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(store.endpoints))
	}
	if len(store.revSpecs) != 1 {
		t.Fatalf("revisions = %d, want 1", len(store.revSpecs))
	}
	ready := false
	for _, e := range published {
		if e.Type == message.TypeDeploymentReady {
			ready = true
		}
	}
	if !ready {
		t.Fatal("no deployment_ready event published")
	}
}

func TestWorkerOnCreatedIdempotent(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentReady

	bus := message.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.revSpecs) != 0 {
		t.Fatalf("revisions = %d, want 0 (already READY, must not reprocess)", len(store.revSpecs))
	}
	if len(store.endpoints) != 0 {
		t.Fatalf("endpoints = %d, want 0", len(store.endpoints))
	}
}

func TestWorkerOnCreatedAdapterFailure(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := message.NewMemoryEventBus()
	var published []message.Event
	if err := bus.Subscribe(context.Background(), message.TopicDeploymentEvents,
		func(ctx context.Context, ev message.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, failAdapter{NewWorkerAdapter("localhost:1")}, bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentFailed {
		t.Fatalf("status = %s, want %s", got, DeploymentFailed)
	}
	failed := false
	for _, e := range published {
		if e.Type == message.TypeDeploymentFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no deployment_failed event published")
	}
}

// flakyAdapter fails Start a fixed number of times, then delegates to the real
// WorkerAdapter (which validates the spec). Exercises the A5 retry path: a
// transient runtime blip must not permanently fail the deployment.
type flakyAdapter struct {
	*WorkerAdapter
	mu    sync.Mutex
	fails int
}

func (a *flakyAdapter) Start(ctx context.Context, d *Deployment) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fails > 0 {
		a.fails--
		return errors.New("runtime temporarily unreachable")
	}
	return a.WorkerAdapter.Start(ctx, d)
}

func TestWorkerOnCreatedTransientAdapterStartRecovers(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := message.NewMemoryEventBus()
	var published []message.Event
	if err := bus.Subscribe(context.Background(), message.TopicDeploymentEvents,
		func(ctx context.Context, ev message.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, &flakyAdapter{WorkerAdapter: NewWorkerAdapter("localhost:1"), fails: 2}, bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentReady {
		t.Fatalf("status = %s, want %s (transient failure must not fail deployment)", got, DeploymentReady)
	}
	for _, e := range published {
		if e.Type == message.TypeDeploymentFailed {
			t.Fatal("deployment_failed published after transient retry; want recovery to READY")
		}
	}
}

// countingAdapter always fails Start and counts invocations, to prove the
// circuit breaker fails fast: once open, Start must not be called again.
type countingAdapter struct {
	*WorkerAdapter
	mu     sync.Mutex
	starts int
}

func (a *countingAdapter) Start(ctx context.Context, d *Deployment) error {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	return errors.New("runtime exploded")
}

func TestWorkerCircuitBreakerFailsFast(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	bus := message.NewMemoryEventBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapter := &countingAdapter{WorkerAdapter: NewWorkerAdapter("localhost:1")}
	w := NewWorker(store, adapter, NewMockComputeProvider(), bus, bus, log)
	// Open the breaker after a single failed deployment so the second is fast.
	w.cb = circuitbreaker.New(1, time.Minute)

	for i := 0; i < 2; i++ {
		store.deployments["d1"] = validDeployment()
		ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
		if err := w.handle(ctx, ev); err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
		store.mu.Lock()
		st := store.deployments["d1"].Status
		store.mu.Unlock()
		if st != DeploymentFailed {
			t.Fatalf("deployment %d status = %s, want FAILED", i, st)
		}
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.starts != retryAttempts {
		t.Fatalf("adapter.Start calls = %d, want %d (only first deployment retries; second must fail fast)",
			adapter.starts, retryAttempts)
	}
}

func TestWorkerOnStopHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentReady

	bus := message.NewMemoryEventBus()
	var published []message.Event
	if err := bus.Subscribe(context.Background(), message.TopicDeploymentEvents,
		func(ctx context.Context, ev message.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentStopped {
		t.Fatalf("status = %s, want %s", got, DeploymentStopped)
	}
	stopped := false
	for _, e := range published {
		if e.Type == message.TypeDeploymentStopped {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no deployment_stopped event published")
	}
}

func TestWorkerOnStopAlreadyStopped(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentStopped

	bus := message.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentStopped {
		t.Fatalf("status = %s, want %s (unchanged)", got, DeploymentStopped)
	}
}

func TestWorkerOnStopDuringProvisioning(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentProvisioning

	bus := message.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle stop during provisioning = %v, want nil (idempotent no-op)", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentProvisioning {
		t.Fatalf("status = %s, want %s (unchanged)", got, DeploymentProvisioning)
	}
}

func TestWorkerOnCreatedFailureReleasesCapacity(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	compute := NewMockComputeProvider()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := message.NewMemoryEventBus()
	w := NewWorker(store, failAdapter{NewWorkerAdapter("localhost:1")}, compute, bus, bus, log)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, err := compute.GetWorkloadStatus(context.Background(), store.deployments["d1"]); err == nil {
		t.Fatal("capacity not released after start failure")
	}
}

func TestWorkerOnCreatedResumeFromProvisioning(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentProvisioning

	bus := message.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentReady {
		t.Fatalf("status = %s, want %s", got, DeploymentReady)
	}
	if len(store.revSpecs) != 0 {
		t.Fatalf("revisions = %d, want 0 (resume must not re-create revision)", len(store.revSpecs))
	}
	if store.workloadRef == "" {
		t.Fatal("workload_ref not set on resume")
	}
	if len(store.endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(store.endpoints))
	}
}

func TestWorkerOnCreatedResumeFromStarting(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = DeploymentStarting
	store.deployments["d1"].WorkloadRef = "mock-wl-d1"

	bus := message.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := message.NewEvent(message.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != DeploymentReady {
		t.Fatalf("status = %s, want %s", got, DeploymentReady)
	}
	if len(store.revSpecs) != 0 {
		t.Fatalf("revisions = %d, want 0", len(store.revSpecs))
	}
	if len(store.endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(store.endpoints))
	}
}
