package serving

import (
	"log/slog"
	"net/http"

	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler mounts the serving routes: models, templates, deployments.
type Handler struct {
	svc    *Service
	auth   middleware.Authenticator
	events EventSink
}

func NewHandler(svc *Service, auth middleware.Authenticator, events EventSink) *Handler {
	return &Handler{svc: svc, auth: auth, events: events}
}

// --- models ---

func (h *Handler) handleCreateModel(c *gin.Context) {
	var m Model
	if err := c.ShouldBindJSON(&m); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.svc.CreateModel(c.Request.Context(), m)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, created)
}

func (h *Handler) handleListModels(c *gin.Context) {
	ms, err := h.svc.ListModels(c.Request.Context())
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, ms)
}

func (h *Handler) handleGetModel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "id required")
		return
	}
	m, err := h.svc.GetModel(c.Request.Context(), id)
	if err != nil {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "model not found")
		return
	}
	response.WriteJSON(c, http.StatusOK, m)
}

func (h *Handler) handleCreateModelVersion(c *gin.Context) {
	id := c.Param("id")
	var mv ModelVersion
	if err := c.ShouldBindJSON(&mv); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	mv.ModelID = id
	created, err := h.svc.CreateModelVersion(c.Request.Context(), mv)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, created)
}

func (h *Handler) handleListModelVersions(c *gin.Context) {
	vs, err := h.svc.ListModelVersions(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, vs)
}

// --- templates ---

func (h *Handler) handleCreateTemplate(c *gin.Context) {
	var t ServingTemplate
	if err := c.ShouldBindJSON(&t); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.svc.CreateTemplate(c.Request.Context(), t)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, created)
}

func (h *Handler) handleListTemplates(c *gin.Context) {
	ts, err := h.svc.ListTemplates(c.Request.Context())
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, ts)
}

func (h *Handler) handleGetTemplate(c *gin.Context) {
	id := c.Param("id")
	t, err := h.svc.GetTemplate(c.Request.Context(), id)
	if err != nil {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "template not found")
		return
	}
	response.WriteJSON(c, http.StatusOK, t)
}

func (h *Handler) handleCreateTemplateVersion(c *gin.Context) {
	id := c.Param("id")
	var tv TemplateVersion
	if err := c.ShouldBindJSON(&tv); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	tv.TemplateID = id
	created, err := h.svc.CreateTemplateVersion(c.Request.Context(), tv)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, created)
}

func (h *Handler) handleListTemplateVersions(c *gin.Context) {
	vs, err := h.svc.ListTemplateVersions(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, vs)
}

// --- deployments ---

func (h *Handler) handleCreateDeployment(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var d Deployment
	if err := c.ShouldBindJSON(&d); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	d.TenantID = p.TenantID // derive tenant from auth, never trust body

	// Idempotency-Key: a client retry with the same key returns the same
	// deployment instead of creating a duplicate (roadmap A5 — idempotency).
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if existingID, err := h.svc.ResolveIdempotencyKey(c.Request.Context(), p.TenantID, key, "deployment"); err == nil {
			if existing, gerr := h.svc.GetDeployment(c.Request.Context(), existingID); gerr == nil {
				response.WriteJSON(c, http.StatusOK, existing)
				return
			}
		}
	}

	created, err := h.svc.CreateDeploymentWithEvent(c.Request.Context(), d, p.UserID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if err := h.svc.SaveIdempotencyKey(c.Request.Context(), p.TenantID, key, "deployment", created.ID); err != nil {
			slog.Warn("save idempotency key", "err", err) // non-fatal: replay safety is best-effort
		}
	}
	response.WriteJSON(c, http.StatusAccepted, created)
}

func (h *Handler) handleListDeployments(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	ds, err := h.svc.ListDeployments(c.Request.Context(), p.TenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, ds)
}

func (h *Handler) handleDeploymentByID(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	d, err := h.svc.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != p.TenantID {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	response.WriteJSON(c, http.StatusOK, d)
}

func (h *Handler) handleDeploymentRevisions(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	d, err := h.svc.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != p.TenantID {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	revs, err := h.svc.ListRevisions(c.Request.Context(), id)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, revs)
}

func (h *Handler) handleDeploymentAction(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id, action := c.Param("id"), c.Param("action")
	d, err := h.svc.GetDeployment(c.Request.Context(), id)
	if err != nil || d.TenantID != p.TenantID {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	// Async: the worker performs the actual state transitions.
	switch action {
	case "start":
		ev := message.NewEvent(message.TypeDeploymentCreated, p.TenantID, id, map[string]any{
			"name": d.Name, "region": d.Region, "desired_replicas": d.DesiredReplicas,
			"model_version_id": d.ModelVersionID, "template_version_id": d.TemplateVersionID,
			"created_by": p.UserID,
		})
		if err := h.events.Enqueue(c.Request.Context(), message.TopicDeploymentEvents, ev); err != nil {
			response.WriteAPIError(c, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		response.WriteJSON(c, http.StatusAccepted, d)
	case "stop":
		ev := message.NewEvent(message.TypeDeploymentStopRequested, p.TenantID, id, nil)
		if err := h.events.Enqueue(c.Request.Context(), message.TopicDeploymentEvents, ev); err != nil {
			response.WriteAPIError(c, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		response.WriteJSON(c, http.StatusAccepted, d)
	default:
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "unknown action "+action)
		return
	}
}
