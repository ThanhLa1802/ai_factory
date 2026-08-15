package runtime

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/ai-factory/go-server/internal/controlplane"
)

func validDeployment() *controlplane.Deployment {
	return &controlplane.Deployment{
		ID: "d1", TenantID: "t1", Name: "svc",
		ModelVersionID: "mv1", TemplateVersionID: "tv1",
		DesiredReplicas: 1, Status: controlplane.DeploymentPending,
	}
}

func TestWorkerAdapterValidatesSpec(t *testing.T) {
	a := NewWorkerAdapter("localhost:1")
	if err := a.Start(context.Background(), validDeployment()); err != nil {
		t.Fatalf("Start(valid) = %v, want nil", err)
	}
	bad := validDeployment()
	bad.DesiredReplicas = 0
	if err := a.Start(context.Background(), bad); err == nil {
		t.Fatal("Start(desired_replicas=0) = nil, want validation error")
	}
	bad = validDeployment()
	bad.ModelVersionID = ""
	if err := a.Stop(context.Background(), bad); err == nil {
		t.Fatal("Stop(empty model_version_id) = nil, want validation error")
	}
}

func TestWorkerAdapterHealthCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	a := NewWorkerAdapter(addr)
	if err := a.HealthCheck(context.Background(), validDeployment()); err != nil {
		t.Fatalf("HealthCheck(open) = %v, want nil", err)
	}

	dead := NewWorkerAdapter("127.0.0.1:1")
	if err := dead.HealthCheck(context.Background(), validDeployment()); err == nil {
		t.Fatal("HealthCheck(closed port) = nil, want error")
	}
}

func TestMockComputeProviderLifecycle(t *testing.T) {
	m := NewMockComputeProvider()
	ctx := context.Background()
	d := validDeployment()

	ref, err := m.RequestCapacity(ctx, d)
	if err != nil {
		t.Fatalf("RequestCapacity: %v", err)
	}
	if !strings.HasPrefix(ref, "mock-wl-") {
		t.Fatalf("workload ref = %q, want mock-wl- prefix", ref)
	}
	if st, err := m.GetWorkloadStatus(ctx, d); err != nil || st != "RUNNING" {
		t.Fatalf("GetWorkloadStatus = %q, %v", st, err)
	}
	if err := m.ReleaseCapacity(ctx, d); err != nil {
		t.Fatalf("ReleaseCapacity: %v", err)
	}
	if _, err := m.GetWorkloadStatus(ctx, d); err == nil {
		t.Fatal("GetWorkloadStatus after release = nil, want error")
	}
	if err := m.UpdateWorkload(ctx, d); err != nil {
		t.Fatalf("UpdateWorkload: %v", err)
	}
}
