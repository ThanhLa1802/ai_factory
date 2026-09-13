package billing

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler mounts the billing routes (wallet, ledger, pricing, top-up).
type Handler struct {
	svc  *Service
	auth middleware.Authenticator
}

func NewHandler(svc *Service, auth middleware.Authenticator) *Handler {
	return &Handler{svc: svc, auth: auth}
}

func (h *Handler) handleGetWallet(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	w, err := h.svc.GetWallet(c.Request.Context(), p.TenantID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, w)
}

func (h *Handler) handleListLedger(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := h.svc.ListLedger(c.Request.Context(), p.TenantID, limit)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, entries)
}

func (h *Handler) handleListPrices(c *gin.Context) {
	prices, err := h.svc.ListPrices(c.Request.Context())
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, prices)
}

func (h *Handler) handleUpsertPrice(c *gin.Context) {
	var p Price
	if err := c.ShouldBindJSON(&p); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	out, err := h.svc.UpsertPrice(c.Request.Context(), p)
	switch {
	case errors.Is(err, ErrInvalidPrice):
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case err != nil:
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	default:
		response.WriteJSON(c, http.StatusOK, out)
	}
}

func (h *Handler) handleTopUp(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var req struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	tx, err := h.svc.TopUp(c.Request.Context(), p.TenantID, req.Amount, c.GetHeader("Idempotency-Key"))
	switch {
	case errors.Is(err, ErrInvalidAmount), errors.Is(err, ErrInvalidRequest):
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, ErrIdempotencyConflict):
		response.WriteAPIError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case err != nil:
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	default:
		response.WriteJSON(c, http.StatusOK, tx)
	}
}
