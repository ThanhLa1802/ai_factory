package runtime

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// WorkerAdapter wraps the Python inference worker. It has no deployment
// lifecycle RPC (proto only exposes Generate/BatchGenerate), so lifecycle
// operations validate the deployment spec and return; HealthCheck probes the
// worker's gRPC port with a TCP dial.
type WorkerAdapter struct {
	workerAddr string
}

func NewWorkerAdapter(workerAddr string) *WorkerAdapter {
	return &WorkerAdapter{workerAddr: workerAddr}
}

func (a *WorkerAdapter) Create(ctx context.Context, d *controlplane.Deployment) error { return a.validate(d) }
func (a *WorkerAdapter) Start(ctx context.Context, d *controlplane.Deployment) error  { return a.validate(d) }
func (a *WorkerAdapter) Stop(ctx context.Context, d *controlplane.Deployment) error   { return a.validate(d) }
func (a *WorkerAdapter) Restart(ctx context.Context, d *controlplane.Deployment) error {
	return a.validate(d)
}
func (a *WorkerAdapter) Delete(ctx context.Context, d *controlplane.Deployment) error { return nil }

// GetStatus reports the control-plane status back; there is no worker-side
// status to query yet.
func (a *WorkerAdapter) GetStatus(ctx context.Context, d *controlplane.Deployment) (string, error) {
	return d.Status, nil
}

// HealthCheck verifies the worker's gRPC endpoint accepts TCP connections.
func (a *WorkerAdapter) HealthCheck(ctx context.Context, d *controlplane.Deployment) error {
	conn, err := net.DialTimeout("tcp", a.workerAddr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("worker unreachable at %s: %w", a.workerAddr, err)
	}
	_ = conn.Close()
	return nil
}

func (a *WorkerAdapter) validate(d *controlplane.Deployment) error {
	if d.ModelVersionID == "" || d.TemplateVersionID == "" || d.DesiredReplicas < 1 {
		return fmt.Errorf("invalid deployment spec: model_version_id, template_version_id, desired_replicas>=1 required")
	}
	return nil
}
