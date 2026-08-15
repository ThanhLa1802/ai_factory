package controlplane

const (
	DeploymentPending      = "PENDING"
	DeploymentProvisioning = "PROVISIONING"
	DeploymentStarting     = "STARTING"
	DeploymentReady        = "READY"
	DeploymentDegraded     = "DEGRADED"
	DeploymentStopping     = "STOPPING"
	DeploymentStopped      = "STOPPED"
	DeploymentFailed       = "FAILED"
)

// validTransitions maps current state to the set of allowed next states.
var validTransitions = map[string]map[string]bool{
	DeploymentPending:      {DeploymentProvisioning: true, DeploymentFailed: true},
	DeploymentProvisioning: {DeploymentStarting: true, DeploymentFailed: true},
	DeploymentStarting:     {DeploymentReady: true, DeploymentDegraded: true, DeploymentFailed: true},
	DeploymentReady:        {DeploymentDegraded: true, DeploymentStopping: true, DeploymentFailed: true},
	DeploymentDegraded:     {DeploymentReady: true, DeploymentStopping: true, DeploymentFailed: true},
	DeploymentStopping:     {DeploymentStopped: true, DeploymentFailed: true},
	DeploymentStopped:      {DeploymentPending: true},
	DeploymentFailed:       {},
}

// CanTransition reports whether current may move to next.
func CanTransition(current, next string) bool {
	return validTransitions[current][next]
}
