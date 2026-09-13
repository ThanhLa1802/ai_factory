// Package migrations holds the gormigrate migrations for the control plane.
// IDs mirror the numeric goose versions they were converted from; table and
// column names are byte-identical to the previous goose schema.
package migrations

import "github.com/go-gormigrate/gormigrate/v2"

// All returns every migration in application order.
func All() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		M0001Init(),
		M0002DeploymentWorkloadRef(),
		M0003IdempotencyKeys(),
		M0004ChatHistory(),
		M0006UsageEvents(),
		M0007SessionTitle(),
		M0008Outbox(),
		M0009UsageDaily(),
	}
}
