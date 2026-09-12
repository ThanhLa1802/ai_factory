package usage

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the usage/quota routes. Auth middleware is built from
// the neutral Authenticator port supplied by the composition root.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.POST("/api/v1/quotas", middleware.RequirePermission(h.auth, middleware.ActionQuotaManage), h.handleUpsertQuota)
	e.GET("/api/v1/quotas", middleware.RequirePermission(h.auth, middleware.ActionUsageRead), h.handleListQuotas)

	e.GET("/api/v1/usage", middleware.RequirePermission(h.auth, middleware.ActionUsageRead), h.handleGetUsage)
}
