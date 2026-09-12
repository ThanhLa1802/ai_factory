package usage

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler mounts the usage/quota routes.
type Handler struct {
	svc  *Service
	auth middleware.Authenticator
}

func NewHandler(svc *Service, auth middleware.Authenticator) *Handler {
	return &Handler{svc: svc, auth: auth}
}

// --- quotas ---

func (h *Handler) handleUpsertQuota(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var q Quota
	if err := c.ShouldBindJSON(&q); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	q.TenantID = p.TenantID
	created, err := h.svc.UpsertQuota(c.Request.Context(), q)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, created)
}

func (h *Handler) handleListQuotas(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	qs, err := h.svc.ListQuotas(c.Request.Context(), p.TenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, qs)
}

func (h *Handler) handleGetUsage(c *gin.Context) {
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

	today, err := h.svc.UsageSummary(c.Request.Context(), p.TenantID, todayStart, now)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	month, err := h.svc.UsageSummary(c.Request.Context(), p.TenantID, monthStart, now)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	daily, err := h.svc.UsageDaily(c.Request.Context(), p.TenantID, dailyStart, now)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	byModel, err := h.svc.UsageByModel(c.Request.Context(), p.TenantID, monthStart, now)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	response.WriteJSON(c, http.StatusOK, gin.H{
		"today":    today,
		"month":    month,
		"daily":    daily,
		"by_model": byModel,
	})
}
