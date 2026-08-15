package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
)

type fakeStore struct {
	mu          sync.Mutex
	deployments map[string]*controlplane.Deployment
	workloadRef string
	endpoints   []*controlplane.Endpoint
	revSpecs    []map[string]any
}

func newFakeStore() *fakeStore {
	return &fakeStore{deployments: map[string]*controlplane.Deployment{}}
}

func (f *fakeStore) GetDeployment(ctx context.Context, id string) (*controlplane.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	cp := *d
	return &cp, nil
}

func (f *fakeStore) TransitionDeployment(ctx context.Context, id, to string) (*controlplane.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	if !controlplane.CanTransition(d.Status, to) {
		return nil, controlplane.ErrInvalidTransition
	}
	d.Status = to
	cp := *d
	return &cp, nil
}

func (f *fakeStore) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*controlplane.DeploymentRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revSpecs = append(f.revSpecs, spec)
	return &controlplane.DeploymentRevision{
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

func (f *fakeStore) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*controlplane.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := &controlplane.Endpoint{ID: "ep-1", DeploymentID: deploymentID, Path: path, Protocol: protocol, Status: "ACTIVE"}
	f.endpoints = append(f.endpoints, e)
	return e, nil
}

// failAdapter fails Start to exercise the FAILED path.
type failAdapter struct{ *WorkerAdapter }

func (a failAdapter) Start(ctx context.Context, d *controlplane.Deployment) error {
	return errors.New("worker exploded")
}

func newTestWorker(store DeploymentStore, adapter ServingRuntimeAdapter, bus *events.MemoryEventBus) *Worker {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorker(store, adapter, NewMockComputeProvider(), bus, bus, log)
}

func TestWorkerOnCreatedHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", map[string]any{"created_by": "u1"})
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentReady {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentReady)
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
		if e.Type == events.TypeDeploymentReady {
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
	store.deployments["d1"].Status = controlplane.DeploymentReady

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
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

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, failAdapter{NewWorkerAdapter("localhost:1")}, bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentFailed {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentFailed)
	}
	failed := false
	for _, e := range published {
		if e.Type == events.TypeDeploymentFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no deployment_failed event published")
	}
}

func TestWorkerOnStopHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = controlplane.DeploymentReady

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentStopped {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentStopped)
	}
	stopped := false
	for _, e := range published {
		if e.Type == events.TypeDeploymentStopped {
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
	store.deployments["d1"].Status = controlplane.DeploymentStopped

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentStopped {
		t.Fatalf("status = %s, want %s (unchanged)", got, controlplane.DeploymentStopped)
	}
}

func TestWorkerOnStopDuringProvisioning(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = controlplane.DeploymentProvisioning

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle stop during provisioning = %v, want nil (idempotent no-op)", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentProvisioning {
		t.Fatalf("status = %s, want %s (unchanged)", got, controlplane.DeploymentProvisioning)
	}
}

func TestWorkerOnCreatedFailureReleasesCapacity(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	compute := NewMockComputeProvider()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewMemoryEventBus()
	w := NewWorker(store, failAdapter{NewWorkerAdapter("localhost:1")}, compute, bus, bus, log)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
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
	store.deployments["d1"].Status = controlplane.DeploymentProvisioning

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentReady {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentReady)
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
	store.deployments["d1"].Status = controlplane.DeploymentStarting
	store.deployments["d1"].WorkloadRef = "mock-wl-d1"

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentReady {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentReady)
	}
	if len(store.revSpecs) != 0 {
		t.Fatalf("revisions = %d, want 0", len(store.revSpecs))
	}
	if len(store.endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(store.endpoints))
	}
}
