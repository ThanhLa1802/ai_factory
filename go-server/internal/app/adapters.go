package app

import (
	"context"
	"errors"

	"github.com/ai-factory/go-server/internal/services/billing"
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

// billingGate adapts *billing.Service to inference.BillingGate, translating the
// billing sentinel error so the inference layer never imports services/billing
// (design §4.2 / D-P4-2).
type billingGate struct{ svc *billing.Service }

func (a billingGate) Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error) {
	id, err := a.svc.Reserve(ctx, tenantID, model, estInput, maxOutput)
	if errors.Is(err, billing.ErrInsufficientCredits) {
		return "", inference.ErrInsufficientCredits
	}
	return id, err
}

func (a billingGate) Settle(ctx context.Context, reservationID string, promptTokens, completionTokens int) error {
	return a.svc.Settle(ctx, reservationID, promptTokens, completionTokens)
}

func (a billingGate) Release(ctx context.Context, reservationID string) error {
	return a.svc.Release(ctx, reservationID)
}
