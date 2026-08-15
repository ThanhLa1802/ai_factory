package auth

const (
	RolePlatformAdmin   = "PLATFORM_ADMIN"
	RoleTenantAdmin     = "TENANT_ADMIN"
	RoleTenantDeveloper = "TENANT_DEVELOPER"
	RoleTenantViewer    = "TENANT_VIEWER"
)

// Action constants used by RequirePermission middleware.
const (
	ActionTenantManage   = "tenant.manage"
	ActionTenantRead     = "tenant.read"
	ActionModelWrite     = "model.write"
	ActionModelRead      = "model.read"
	ActionTemplateWrite  = "template.write"
	ActionTemplateRead   = "template.read"
	ActionDeployWrite    = "deployment.write"
	ActionDeployRead     = "deployment.read"
	ActionKeyManage      = "key.manage"
	ActionQuotaManage    = "quota.manage"
	ActionUsageRead      = "usage.read"
)

var rolePermissions = map[string]map[string]bool{
	RolePlatformAdmin: {
		ActionTenantManage: true, ActionTenantRead: true,
		ActionModelWrite: true, ActionModelRead: true,
		ActionTemplateWrite: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionKeyManage: true, ActionQuotaManage: true, ActionUsageRead: true,
	},
	RoleTenantAdmin: {
		ActionTenantRead: true, ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionKeyManage: true, ActionQuotaManage: true, ActionUsageRead: true,
	},
	RoleTenantDeveloper: {
		ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionUsageRead: true,
	},
	RoleTenantViewer: {
		ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployRead: true, ActionUsageRead: true,
	},
}

// RoleAllows reports whether role may perform action.
func RoleAllows(role, action string) bool {
	return rolePermissions[role][action]
}
