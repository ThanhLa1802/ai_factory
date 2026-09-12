package serving

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the serving control-plane routes. Auth middleware is
// built from the neutral Authenticator port supplied by the composition root.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.POST("/api/v1/models", middleware.RequirePermission(h.auth, middleware.ActionModelWrite), h.handleCreateModel)
	e.GET("/api/v1/models", middleware.RequirePermission(h.auth, middleware.ActionModelRead), h.handleListModels)
	e.GET("/api/v1/models/:id", middleware.RequirePermission(h.auth, middleware.ActionModelRead), h.handleGetModel)
	e.POST("/api/v1/models/:id/versions", middleware.RequirePermission(h.auth, middleware.ActionModelWrite), h.handleCreateModelVersion)

	e.POST("/api/v1/templates", middleware.RequirePermission(h.auth, middleware.ActionTemplateWrite), h.handleCreateTemplate)
	e.GET("/api/v1/templates", middleware.RequirePermission(h.auth, middleware.ActionTemplateRead), h.handleListTemplates)
	e.GET("/api/v1/templates/:id", middleware.RequirePermission(h.auth, middleware.ActionTemplateRead), h.handleGetTemplate)
	e.POST("/api/v1/templates/:id/versions", middleware.RequirePermission(h.auth, middleware.ActionTemplateWrite), h.handleCreateTemplateVersion)

	e.POST("/api/v1/deployments", middleware.RequirePermission(h.auth, middleware.ActionDeployWrite), h.handleCreateDeployment)
	e.GET("/api/v1/deployments", middleware.RequirePermission(h.auth, middleware.ActionDeployRead), h.handleListDeployments)
	e.GET("/api/v1/deployments/:id", middleware.RequirePermission(h.auth, middleware.ActionDeployRead), h.handleDeploymentByID)
	e.GET("/api/v1/deployments/:id/revisions", middleware.RequirePermission(h.auth, middleware.ActionDeployRead), h.handleDeploymentRevisions)
	e.POST("/api/v1/deployments/:id/:action", middleware.RequirePermission(h.auth, middleware.ActionDeployWrite), h.handleDeploymentAction)
}
