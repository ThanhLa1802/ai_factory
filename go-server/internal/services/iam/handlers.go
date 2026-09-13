package iam

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler mounts the IAM routes: login, API keys, tenants.
type Handler struct {
	svc     *Service
	authSvc *AuthService
	auth    middleware.Authenticator
}

func NewHandler(svc *Service, authSvc *AuthService, auth middleware.Authenticator) *Handler {
	return &Handler{svc: svc, authSvc: authSvc, auth: auth}
}

// --- auth ---

func (h *Handler) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	token, err := h.authSvc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		response.WriteAPIError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	response.WriteJSON(c, http.StatusOK, map[string]string{"access_token": token})
}

// --- api keys ---

func (h *Handler) handleCreateAPIKey(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var req struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	raw, hash := GenerateAPIKey()
	key, err := h.svc.CreateAPIKey(c.Request.Context(), p.TenantID, req.Name, hash, req.ExpiresAt)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, gin.H{"id": key.ID, "tenant_id": key.TenantID, "name": key.Name, "key": raw})
}

func (h *Handler) handleListAPIKeys(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	keys, err := h.svc.ListAPIKeys(c.Request.Context(), p.TenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, keys)
}

func (h *Handler) handleDeleteAPIKey(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	hash, err := h.svc.DeleteAPIKey(c.Request.Context(), id, p.TenantID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			response.WriteAPIError(c, http.StatusNotFound, "NOT_FOUND", "api key not found")
			return
		}
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if err := h.authSvc.InvalidateAPIKey(c.Request.Context(), hash); err != nil {
		slog.Warn("invalidate api key cache", "err", err) // best-effort
	}
	c.Status(http.StatusNoContent)
}

// --- tenants ---

func (h *Handler) handleCreateTenant(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "name required")
		return
	}
	t, err := h.svc.CreateTenant(c.Request.Context(), req.Name)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, t)
}

func (h *Handler) handleListTenants(c *gin.Context) {
	tenants, err := h.svc.ListTenants(c.Request.Context())
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if tenants == nil {
		tenants = []Tenant{} // encode as [] rather than null
	}
	response.WriteJSON(c, http.StatusOK, tenants)
}

// --- users ---

func (h *Handler) handleCreateUser(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
		TenantID string `json:"tenant_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" || req.TenantID == "" {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "username, password and tenant_id required")
		return
	}
	if req.Role == "" {
		req.Role = RoleTenantViewer
	}
	u, err := h.svc.CreateUserWithPassword(c.Request.Context(), req.Username, req.Email, req.Password, req.Role, req.TenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusCreated, u)
}

func (h *Handler) handleListUsers(c *gin.Context) {
	tenantID := c.Query("tenant_id")
	if tenantID == "" {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "tenant_id required")
		return
	}
	us, err := h.svc.ListUsers(c.Request.Context(), tenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if us == nil {
		us = []User{} // encode as [] rather than null
	}
	response.WriteJSON(c, http.StatusOK, us)
}
