package iam

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the IAM control-plane routes. Auth middleware is built
// from the neutral Authenticator port supplied by the composition root.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.POST("/api/v1/auth/login", h.handleLogin)

	keys := e.Group("/api/v1/api-keys", middleware.RequirePermission(h.auth, middleware.ActionKeyManage))
	keys.POST("", h.handleCreateAPIKey)
	keys.GET("", h.handleListAPIKeys)
	keys.POST("/:id/revoke", h.handleRevokeAPIKey)

	tenants := e.Group("/api/v1/tenants")
	tenants.POST("", middleware.RequirePermission(h.auth, middleware.ActionTenantManage), h.handleCreateTenant)
	tenants.GET("", middleware.RequirePermission(h.auth, middleware.ActionTenantRead), h.handleListTenants)

	users := e.Group("/api/v1/users", middleware.RequirePermission(h.auth, middleware.ActionTenantManage))
	users.POST("", h.handleCreateUser)
	users.GET("", h.handleListUsers)
}
