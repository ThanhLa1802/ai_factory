package auth

import "testing"

func TestRoleAllows(t *testing.T) {
	cases := []struct {
		role, action string
		want         bool
	}{
		{RoleTenantViewer, "deployment.read", true},
		{RoleTenantViewer, "deployment.write", false},
		{RoleTenantDeveloper, "deployment.write", true},
		{RoleTenantDeveloper, "key.manage", false},
		{RoleTenantAdmin, "key.manage", true},
		{RoleTenantAdmin, "template.write", false},
		{RolePlatformAdmin, "template.write", true},
		{RolePlatformAdmin, "tenant.manage", true},
		{"UNKNOWN", "deployment.read", false},
	}
	for _, c := range cases {
		if got := RoleAllows(c.role, c.action); got != c.want {
			t.Errorf("RoleAllows(%q, %q) = %v, want %v", c.role, c.action, got, c.want)
		}
	}
}
