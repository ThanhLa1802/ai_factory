package controlplane

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		cur, next string
		want      bool
	}{
		{DeploymentPending, DeploymentProvisioning, true},
		{DeploymentProvisioning, DeploymentStarting, true},
		{DeploymentStarting, DeploymentReady, true},
		{DeploymentStarting, DeploymentDegraded, true},
		{DeploymentReady, DeploymentDegraded, true},
		{DeploymentReady, DeploymentStopping, true},
		{DeploymentDegraded, DeploymentReady, true},
		{DeploymentStopping, DeploymentStopped, true},
		{DeploymentPending, DeploymentFailed, true},
		{DeploymentStopped, DeploymentPending, true},
		{DeploymentReady, DeploymentPending, false},
		{DeploymentFailed, DeploymentReady, false},
		{DeploymentPending, DeploymentReady, false},
		{"BOGUS", DeploymentReady, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.cur, c.next); got != c.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", c.cur, c.next, got, c.want)
		}
	}
}
