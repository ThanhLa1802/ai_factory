package app

import (
	"context"

	"github.com/ai-factory/go-server/internal/services/inference"
	"github.com/ai-factory/go-server/internal/services/serving"
)

// deploymentResolver adapts serving.Service to the inference handler's
// consumer-defined DeploymentResolver. It is the only place the two services
// meet (design §4.2 / D-P4-2).
type deploymentResolver struct{ svc *serving.Service }

func (a deploymentResolver) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*inference.ResolvedDeployment, error) {
	d, err := a.svc.ResolveDeployment(ctx, tenantID, modelName)
	if err != nil {
		return nil, err
	}
	return &inference.ResolvedDeployment{ID: d.ID, TenantID: d.TenantID, Region: d.Region}, nil
}
