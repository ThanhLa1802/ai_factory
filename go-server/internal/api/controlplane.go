package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/gin-gonic/gin"
)

// ControlPlaneHandler mounts /api/v1/* routes.
type ControlPlaneHandler struct {
	cp       *controlplane.Service
	auth     *auth.Service
	secret   []byte
	producer events.Producer
}

func NewControlPlaneHandler(cp *controlplane.Service, authSvc *auth.Service, secret []byte, producer events.Producer) *ControlPlaneHandler {
	return &ControlPlaneHandler{cp: cp, auth: authSvc, secret: secret, producer: producer}
}

// RegisterRoutes mounts all control plane routes.
func (h *ControlPlaneHandler) RegisterRoutes(e *gin.Engine) {
	e.POST("/api/v1/auth/login", h.handleLogin)

	keys := e.Group("/api/v1/api-keys", auth.RequirePermission(h.secret, auth.ActionKeyManage))
	keys.POST("", h.handleCreateAPIKey)
	keys.GET("", h.handleListAPIKeys)
	keys.DELETE("/:id", h.handleDeleteAPIKey)

	tenants := e.Group("/api/v1/tenants")
	tenants.POST("", auth.RequirePermission(h.secret, auth.ActionTenantManage), h.handleCreateTenant)
	tenants.GET("", auth.RequirePermission(h.secret, auth.ActionTenantRead), h.handleListTenants)

	e.POST("/api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelWrite), h.handleCreateModel)
	e.GET("/api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelRead), h.handleListModels)
	e.GET("/api/v1/models/:id", auth.RequirePermission(h.secret, auth.ActionModelRead), h.handleGetModel)
	e.POST("/api/v1/models/:id/versions", auth.RequirePermission(h.secret, auth.ActionModelWrite), h.handleCreateModelVersion)

	e.POST("/api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateWrite), h.handleCreateTemplate)
	e.GET("/api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateRead), h.handleListTemplates)
	e.GET("/api/v1/templates/:id", auth.RequirePermission(h.secret, auth.ActionTemplateRead), h.handleGetTemplate)
	e.POST("/api/v1/templates/:id/versions", auth.RequirePermission(h.secret, auth.ActionTemplateWrite), h.handleCreateTemplateVersion)

	e.POST("/api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployWrite), h.handleCreateDeployment)
	e.GET("/api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployRead), h.handleListDeployments)
	e.GET("/api/v1/deployments/:id", auth.RequirePermission(h.secret, auth.ActionDeployRead), h.handleDeploymentByID)
	e.GET("/api/v1/deployments/:id/revisions", auth.RequirePermission(h.secret, auth.ActionDeployRead), h.handleDeploymentRevisions)
	e.POST("/api/v1/deployments/:id/:action", auth.RequirePermission(h.secret, auth.ActionDeployWrite), h.handleDeploymentAction)

	e.POST("/api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionQuotaManage), h.handleUpsertQuota)
	e.GET("/api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionUsageRead), h.handleListQuotas)

	e.GET("/api/v1/usage", auth.RequirePermission(h.secret, auth.ActionUsageRead), h.handleGetUsage)
}

// --- auth ---

func (h *ControlPlaneHandler) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	token, err := h.auth.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		writeAPIError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	writeJSON(c, http.StatusOK, map[string]string{"access_token": token})
}

func (h *ControlPlaneHandler) handleCreateAPIKey(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	var req struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	raw, hash := auth.GenerateAPIKey()
	key, err := h.cp.CreateAPIKey(c.Request.Context(), claims.TenantID, req.Name, hash, req.ExpiresAt)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, gin.H{"id": key.ID, "tenant_id": key.TenantID, "name": key.Name, "key": raw})
}

func (h *ControlPlaneHandler) handleListAPIKeys(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	keys, err := h.cp.ListAPIKeys(c.Request.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, keys)
}

func (h *ControlPlaneHandler) handleDeleteAPIKey(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	id := c.Param("id")
	if err := h.cp.DeleteAPIKey(c.Request.Context(), id, claims.TenantID); err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			writeAPIError(c, http.StatusNotFound, "NOT_FOUND", "api key not found")
			return
		}
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

// --- tenants ---

func (h *ControlPlaneHandler) handleCreateTenant(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "name required")
		return
	}
	t, err := h.cp.CreateTenant(c.Request.Context(), req.Name)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, t)
}

func (h *ControlPlaneHandler) handleListTenants(c *gin.Context) {
	tenants, err := h.cp.ListTenants(c.Request.Context())
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, tenants)
}

// --- models ---

func (h *ControlPlaneHandler) handleCreateModel(c *gin.Context) {
	var m controlplane.Model
	if err := c.ShouldBindJSON(&m); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.cp.CreateModel(c.Request.Context(), m)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, created)
}

func (h *ControlPlaneHandler) handleListModels(c *gin.Context) {
	ms, err := h.cp.ListModels(c.Request.Context())
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, ms)
}

func (h *ControlPlaneHandler) handleGetModel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "id required")
		return
	}
	m, err := h.cp.GetModel(c.Request.Context(), id)
	if err != nil {
		writeAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "model not found")
		return
	}
	writeJSON(c, http.StatusOK, m)
}

func (h *ControlPlaneHandler) handleCreateModelVersion(c *gin.Context) {
	id := c.Param("id")
	var mv controlplane.ModelVersion
	if err := c.ShouldBindJSON(&mv); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	mv.ModelID = id
	created, err := h.cp.CreateModelVersion(c.Request.Context(), mv)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, created)
}

// --- templates ---

func (h *ControlPlaneHandler) handleCreateTemplate(c *gin.Context) {
	var t controlplane.ServingTemplate
	if err := c.ShouldBindJSON(&t); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.cp.CreateTemplate(c.Request.Context(), t)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, created)
}

func (h *ControlPlaneHandler) handleListTemplates(c *gin.Context) {
	ts, err := h.cp.ListTemplates(c.Request.Context())
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, ts)
}

func (h *ControlPlaneHandler) handleGetTemplate(c *gin.Context) {
	id := c.Param("id")
	t, err := h.cp.GetTemplate(c.Request.Context(), id)
	if err != nil {
		writeAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "template not found")
		return
	}
	writeJSON(c, http.StatusOK, t)
}

func (h *ControlPlaneHandler) handleCreateTemplateVersion(c *gin.Context) {
	id := c.Param("id")
	var tv controlplane.TemplateVersion
	if err := c.ShouldBindJSON(&tv); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	tv.TemplateID = id
	created, err := h.cp.CreateTemplateVersion(c.Request.Context(), tv)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusCreated, created)
}

// --- deployments ---

func (h *ControlPlaneHandler) handleCreateDeployment(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	var d controlplane.Deployment
	if err := c.ShouldBindJSON(&d); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	d.TenantID = claims.TenantID // derive tenant from auth, never trust body

	// Idempotency-Key: a client retry with the same key returns the same
	// deployment instead of creating a duplicate (roadmap A5 — idempotency).
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if existingID, err := h.cp.ResolveIdempotencyKey(c.Request.Context(), claims.TenantID, key, "deployment"); err == nil {
			if existing, gerr := h.cp.GetDeployment(c.Request.Context(), existingID); gerr == nil {
				writeJSON(c, http.StatusOK, existing)
				return
			}
		}
	}

	created, err := h.cp.CreateDeployment(c.Request.Context(), d)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if err := h.cp.SaveIdempotencyKey(c.Request.Context(), claims.TenantID, key, "deployment", created.ID); err != nil {
			slog.Warn("save idempotency key", "err", err) // non-fatal: replay safety is best-effort
		}
	}

	ev := events.NewEvent(events.TypeDeploymentCreated, claims.TenantID, created.ID, map[string]any{
		"name":                created.Name,
		"region":              created.Region,
		"desired_replicas":    created.DesiredReplicas,
		"model_version_id":    created.ModelVersionID,
		"template_version_id": created.TemplateVersionID,
		"created_by":          claims.UserID,
	})
	if err := h.producer.Publish(c.Request.Context(), events.TopicDeploymentEvents, ev); err != nil {
		// The deployment is persisted but not queued. Mark it FAILED so it is not
		// left stuck in PENDING (best-effort), then surface the error loudly.
		_, _ = h.cp.TransitionDeployment(c.Request.Context(), created.ID, controlplane.DeploymentFailed)
		writeAPIError(c, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", "deployment persisted but event publish failed")
		return
	}
	writeJSON(c, http.StatusAccepted, created)
}

func (h *ControlPlaneHandler) handleListDeployments(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	ds, err := h.cp.ListDeployments(c.Request.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, ds)
}

func (h *ControlPlaneHandler) handleDeploymentByID(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	id := c.Param("id")
	d, err := h.cp.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	writeJSON(c, http.StatusOK, d)
}

func (h *ControlPlaneHandler) handleDeploymentRevisions(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	id := c.Param("id")
	d, err := h.cp.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	revs, err := h.cp.ListRevisions(c.Request.Context(), id)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, revs)
}

func (h *ControlPlaneHandler) handleDeploymentAction(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	id, action := c.Param("id"), c.Param("action")
	d, err := h.cp.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	// Async: the worker performs the actual state transitions.
	switch action {
	case "start":
		ev := events.NewEvent(events.TypeDeploymentCreated, claims.TenantID, id, map[string]any{
			"name": d.Name, "region": d.Region, "desired_replicas": d.DesiredReplicas,
			"model_version_id": d.ModelVersionID, "template_version_id": d.TemplateVersionID,
			"created_by": claims.UserID,
		})
		if err := h.producer.Publish(c.Request.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(c, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(c, http.StatusAccepted, d)
	case "stop":
		ev := events.NewEvent(events.TypeDeploymentStopRequested, claims.TenantID, id, nil)
		if err := h.producer.Publish(c.Request.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(c, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(c, http.StatusAccepted, d)
	default:
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "unknown action "+action)
		return
	}
}

// --- quotas ---

func (h *ControlPlaneHandler) handleUpsertQuota(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	var q controlplane.Quota
	if err := c.ShouldBindJSON(&q); err != nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	q.TenantID = claims.TenantID
	created, err := h.cp.UpsertQuota(c.Request.Context(), q)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, created)
}

func (h *ControlPlaneHandler) handleListQuotas(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)
	qs, err := h.cp.ListQuotas(c.Request.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(c, http.StatusOK, qs)
}

func (h *ControlPlaneHandler) handleGetUsage(c *gin.Context) {
	claims, _ := auth.ClaimsFromContext(c)

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

	today, err := h.cp.UsageSummary(c.Request.Context(), claims.TenantID, todayStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	month, err := h.cp.UsageSummary(c.Request.Context(), claims.TenantID, monthStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	daily, err := h.cp.UsageDaily(c.Request.Context(), claims.TenantID, dailyStart, now)
	if err != nil {
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	byModel, err := h.cp.UsageByModel(c.Request.Context(), claims.TenantID, monthStart, now)
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
