package billing

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the billing routes. Auth middleware is built from the
// neutral Authenticator port supplied by the composition root.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.GET("/api/v1/billing/wallet", middleware.RequirePermission(h.auth, middleware.ActionBillingRead), h.handleGetWallet)
	e.GET("/api/v1/billing/ledger", middleware.RequirePermission(h.auth, middleware.ActionBillingRead), h.handleListLedger)
	e.GET("/api/v1/billing/pricing", middleware.RequirePermission(h.auth, middleware.ActionBillingRead), h.handleListPrices)
	e.POST("/api/v1/billing/topup", middleware.RequirePermission(h.auth, middleware.ActionBillingManage), h.handleTopUp)
	e.PUT("/api/v1/billing/pricing", middleware.RequirePermission(h.auth, middleware.ActionBillingManage), h.handleUpsertPrice)
}
