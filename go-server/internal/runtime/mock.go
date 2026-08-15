package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// MockComputeProvider is a dev/unit-test compute provider that tracks a
// workload ref per deployment in memory (spec §3.4).
type MockComputeProvider struct {
	mu        sync.Mutex
	workloads map[string]string // deployment ID -> workload ref
}

func NewMockComputeProvider() *MockComputeProvider {
	return &MockComputeProvider{workloads: map[string]string{}}
}

func (m *MockComputeProvider) RequestCapacity(ctx context.Context, d *controlplane.Deployment) (string, error) {
	ref := "mock-wl-" + d.ID
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workloads[d.ID] = ref
	return ref, nil
}

func (m *MockComputeProvider) ReleaseCapacity(ctx context.Context, d *controlplane.Deployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workloads, d.ID)
	return nil
}

func (m *MockComputeProvider) GetWorkloadStatus(ctx context.Context, d *controlplane.Deployment) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workloads[d.ID]; !ok {
		return "", fmt.Errorf("no workload for deployment %s", d.ID)
	}
	return "RUNNING", nil
}

func (m *MockComputeProvider) UpdateWorkload(ctx context.Context, d *controlplane.Deployment) error {
	return nil
}
