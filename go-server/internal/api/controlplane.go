package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// ControlPlaneHandler mounts the usage/quota routes.
type ControlPlaneHandler struct {
	cp   *controlplane.Service
	auth middleware.Authenticator
}

func NewControlPlaneHandler(cp *controlplane.Service, auth middleware.Authenticator) *ControlPlaneHandler {
	return &ControlPlaneHandler{cp: cp, auth: auth}
}

// RegisterRoutes mounts the usage/quota routes.
func (h *ControlPlaneHandler) RegisterRoutes(e *gin.Engine) {
	e.POST("/api/v1/quotas", middleware.RequirePermission(h.auth, middleware.ActionQuotaManage), h.handleUpsertQuota)
	e.GET("/api/v1/quotas", middleware.RequirePermission(h.auth, middleware.ActionUsageRead), h.handleListQuotas)

	e.GET("/api/v1/usage", middleware.RequirePermission(h.auth, middleware.ActionUsageRead), h.handleGetUsage)
}

// --- quotas ---

func (h *ControlPlaneHandler) handleUpsertQuota(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var q controlplane.Quota
	if err := c.ShouldBindJSON(&q); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	q.TenantID = p.TenantID
	created, err := h.cp.UpsertQuota(c.Request.Context(), q)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, created)
}

func (h *ControlPlaneHandler) handleListQuotas(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	qs, err := h.cp.ListQuotas(c.Request.Context(), p.TenantID)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, qs)
}

func (h *ControlPlaneHandler) handleGetUsage(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)

	days := 30
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 90 {
			days = n
		}
	}

	// +5s tolerance on `now`: the DB clock may be slightly ahead of the app host
	// (WSL2/Docker clock drift); without it, just-inserted usage rows can fall
	// outside the exclusive `to` bounds and be missed.
	now := time.Now().UTC().Add(5 * time.Second)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := now.AddDate(0, 0, -30)
	dailyStart := now.AddDate(0, 0, -(days - 1))

	today, err := h.cp.UsageSummary(c.Request.Context(), p.TenantID, todayStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	month, err := h.cp.UsageSummary(c.Request.Context(), p.TenantID, monthStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	daily, err := h.cp.UsageDaily(c.Request.Context(), p.TenantID, dailyStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	byModel, err := h.cp.UsageByModel(c.Request.Context(), p.TenantID, monthStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeJSON(c, http.StatusOK, gin.H{
		"today":    today,
		"month":    month,
		"daily":    daily,
		"by_model": byModel,
	})
}
