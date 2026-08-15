package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
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
func (h *ControlPlaneHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.handleLogin)

	mux.Handle("POST /api/v1/api-keys", auth.RequirePermission(h.secret, auth.ActionKeyManage)(http.HandlerFunc(h.handleCreateAPIKey)))
	mux.Handle("GET /api/v1/api-keys", auth.RequirePermission(h.secret, auth.ActionKeyManage)(http.HandlerFunc(h.handleListAPIKeys)))
	mux.Handle("DELETE /api/v1/api-keys/", auth.RequirePermission(h.secret, auth.ActionKeyManage)(http.HandlerFunc(h.handleDeleteAPIKey)))

	mux.Handle("POST /api/v1/tenants", auth.RequirePermission(h.secret, auth.ActionTenantManage)(http.HandlerFunc(h.handleCreateTenant)))
	mux.Handle("GET /api/v1/tenants", auth.RequirePermission(h.secret, auth.ActionTenantRead)(http.HandlerFunc(h.handleListTenants)))

	mux.Handle("POST /api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelWrite)(http.HandlerFunc(h.handleCreateModel)))
	mux.Handle("GET /api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelRead)(http.HandlerFunc(h.handleListModels)))
	mux.Handle("GET /api/v1/models/", auth.RequirePermission(h.secret, auth.ActionModelRead)(http.HandlerFunc(h.handleGetModel)))
	mux.Handle("POST /api/v1/models/", auth.RequirePermission(h.secret, auth.ActionModelWrite)(http.HandlerFunc(h.handleCreateModelVersion)))

	mux.Handle("POST /api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateWrite)(http.HandlerFunc(h.handleCreateTemplate)))
	mux.Handle("GET /api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateRead)(http.HandlerFunc(h.handleListTemplates)))
	mux.Handle("GET /api/v1/templates/", auth.RequirePermission(h.secret, auth.ActionTemplateRead)(http.HandlerFunc(h.handleGetTemplate)))
	mux.Handle("POST /api/v1/templates/", auth.RequirePermission(h.secret, auth.ActionTemplateWrite)(http.HandlerFunc(h.handleCreateTemplateVersion)))

	mux.Handle("POST /api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployWrite)(http.HandlerFunc(h.handleCreateDeployment)))
	mux.Handle("GET /api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployRead)(http.HandlerFunc(h.handleListDeployments)))
	mux.Handle("GET /api/v1/deployments/", auth.RequirePermission(h.secret, auth.ActionDeployRead)(http.HandlerFunc(h.handleDeploymentByID)))
	mux.Handle("POST /api/v1/deployments/", auth.RequirePermission(h.secret, auth.ActionDeployWrite)(http.HandlerFunc(h.handleDeploymentAction)))

	mux.Handle("POST /api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionQuotaManage)(http.HandlerFunc(h.handleUpsertQuota)))
	mux.Handle("GET /api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionUsageRead)(http.HandlerFunc(h.handleListQuotas)))
}

// --- auth ---

func (h *ControlPlaneHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	token, err := h.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token})
}

func (h *ControlPlaneHandler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var req struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	raw, hash := auth.GenerateAPIKey()
	key, err := h.cp.CreateAPIKey(r.Context(), claims.TenantID, req.Name, hash, req.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": key.ID, "tenant_id": key.TenantID, "name": key.Name, "key": raw})
}

func (h *ControlPlaneHandler) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	keys, err := h.cp.ListAPIKeys(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *ControlPlaneHandler) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/api-keys/")
	if err := h.cp.DeleteAPIKey(r.Context(), id, claims.TenantID); err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "api key not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- tenants ---

func (h *ControlPlaneHandler) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "name required")
		return
	}
	t, err := h.cp.CreateTenant(r.Context(), req.Name)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *ControlPlaneHandler) handleListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.cp.ListTenants(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

// --- models ---

func (h *ControlPlaneHandler) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var m controlplane.Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.cp.CreateModel(r.Context(), m)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *ControlPlaneHandler) handleListModels(w http.ResponseWriter, r *http.Request) {
	ms, err := h.cp.ListModels(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (h *ControlPlaneHandler) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "id required")
		return
	}
	m, err := h.cp.GetModel(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "model not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *ControlPlaneHandler) handleCreateModelVersion(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
	id = strings.TrimSuffix(id, "/versions")
	var mv controlplane.ModelVersion
	if err := json.NewDecoder(r.Body).Decode(&mv); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	mv.ModelID = id
	created, err := h.cp.CreateModelVersion(r.Context(), mv)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// --- templates ---

func (h *ControlPlaneHandler) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var t controlplane.ServingTemplate
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	created, err := h.cp.CreateTemplate(r.Context(), t)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *ControlPlaneHandler) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := h.cp.ListTemplates(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (h *ControlPlaneHandler) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	t, err := h.cp.GetTemplate(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "template not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *ControlPlaneHandler) handleCreateTemplateVersion(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	id = strings.TrimSuffix(id, "/versions")
	var tv controlplane.TemplateVersion
	if err := json.NewDecoder(r.Body).Decode(&tv); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	tv.TemplateID = id
	created, err := h.cp.CreateTemplateVersion(r.Context(), tv)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// --- deployments ---

func (h *ControlPlaneHandler) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var d controlplane.Deployment
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	d.TenantID = claims.TenantID // derive tenant from auth, never trust body

	// Idempotency-Key: a client retry with the same key returns the same
	// deployment instead of creating a duplicate (roadmap A5 — idempotency).
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if existingID, err := h.cp.ResolveIdempotencyKey(r.Context(), claims.TenantID, key, "deployment"); err == nil {
			if existing, gerr := h.cp.GetDeployment(r.Context(), existingID); gerr == nil {
				writeJSON(w, http.StatusOK, existing)
				return
			}
		}
	}

	created, err := h.cp.CreateDeployment(r.Context(), d)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if err := h.cp.SaveIdempotencyKey(r.Context(), claims.TenantID, key, "deployment", created.ID); err != nil {
			log.Printf("warn: save idempotency key: %v", err) // non-fatal: replay safety is best-effort
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
	if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
		// The deployment is persisted but not queued. Mark it FAILED so it is not
		// left stuck in PENDING (best-effort), then surface the error loudly.
		_, _ = h.cp.TransitionDeployment(r.Context(), created.ID, controlplane.DeploymentFailed)
		writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", "deployment persisted but event publish failed")
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}

func (h *ControlPlaneHandler) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	ds, err := h.cp.ListDeployments(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (h *ControlPlaneHandler) handleDeploymentByID(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	id = strings.TrimSuffix(id, "/revisions")
	d, err := h.cp.GetDeployment(r.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/revisions") {
		revs, err := h.cp.ListRevisions(r.Context(), id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, revs)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *ControlPlaneHandler) handleDeploymentAction(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "bad path")
		return
	}
	id, action := parts[0], parts[1]
	d, err := h.cp.GetDeployment(r.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
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
		if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, d)
	case "stop":
		ev := events.NewEvent(events.TypeDeploymentStopRequested, claims.TenantID, id, nil)
		if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, d)
	default:
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "unknown action "+action)
		return
	}
}

// --- quotas ---

func (h *ControlPlaneHandler) handleUpsertQuota(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var q controlplane.Quota
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	q.TenantID = claims.TenantID
	created, err := h.cp.UpsertQuota(r.Context(), q)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (h *ControlPlaneHandler) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	qs, err := h.cp.ListQuotas(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, qs)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
