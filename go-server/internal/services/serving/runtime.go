// Package serving owns the serving catalog, deployments, and their runtime
// lifecycle (spec §3.3 ServingRuntimeAdapter, §3.4 ComputeProvider).
package serving

import "context"

// ServingRuntimeAdapter drives a deployment's lifecycle on the serving
// runtime. The Python worker has no deployment RPC yet (M2 boundary) — the
// concrete WorkerAdapter validates the spec and reports the control-plane
// status; real provisioning lands with a future worker API.
type ServingRuntimeAdapter interface {
	Create(ctx context.Context, d *Deployment) error
	Start(ctx context.Context, d *Deployment) error
	Stop(ctx context.Context, d *Deployment) error
	Restart(ctx context.Context, d *Deployment) error
	Delete(ctx context.Context, d *Deployment) error
	GetStatus(ctx context.Context, d *Deployment) (string, error)
	HealthCheck(ctx context.Context, d *Deployment) error
}

// ComputeProvider acquires and releases the compute backing a deployment.
// RequestCapacity returns an opaque workload reference bound to the deployment.
type ComputeProvider interface {
	RequestCapacity(ctx context.Context, d *Deployment) (string, error)
	ReleaseCapacity(ctx context.Context, d *Deployment) error
	GetWorkloadStatus(ctx context.Context, d *Deployment) (string, error)
	UpdateWorkload(ctx context.Context, d *Deployment) error
}
