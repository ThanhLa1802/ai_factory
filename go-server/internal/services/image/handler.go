package image

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// Handler mounts the image-generation routes.
type Handler struct {
	svc  *Service
	auth middleware.Authenticator
}

// NewHandler builds the HTTP handler.
func NewHandler(svc *Service, auth middleware.Authenticator) *Handler {
	return &Handler{svc: svc, auth: auth}
}

// --- OpenAI request/response ---

type openAIImageRequest struct {
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	NegativePrompt string   `json:"negative_prompt"`
	N              *int     `json:"n"`
	Size           string   `json:"size"`
	Steps          *int     `json:"steps"`
	GuidanceScale  *float64 `json:"guidance_scale"`
	Seed           *int64   `json:"seed"`
	Scheduler      string   `json:"scheduler"`
	ResponseFormat string   `json:"response_format"`
}

type openAIImageResponse struct {
	Created int64         `json:"created"`
	Data    []openAIImage `json:"data"`
}

type openAIImage struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	// Non-standard metadata extensions (harmless to OpenAI clients) so the UI
	// can show the real seed/size/steps actually used.
	Seed   int64 `json:"seed,omitempty"`
	Width  int   `json:"width,omitempty"`
	Height int   `json:"height,omitempty"`
	Steps  int   `json:"steps,omitempty"`
}

func (r *openAIImageRequest) toParams() (GenerateParams, error) {
	p := GenerateParams{
		Model:          r.Model,
		Prompt:         r.Prompt,
		NegativePrompt: r.NegativePrompt,
		Steps:          intOr(r.Steps, 0),
		GuidanceScale:  floatOr(r.GuidanceScale, -1),
		Seed:           int64Or(r.Seed, -1),
		NumImages:      intOr(r.N, 0),
		Scheduler:      r.Scheduler,
	}
	if r.Size != "" {
		w, h, err := parseSize(r.Size)
		if err != nil {
			return p, err
		}
		p.Width, p.Height = w, h
	}
	return p, nil
}

func parseSize(size string) (int, int, error) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(size)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("size must be WIDTHxHEIGHT")
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, errors.New("size must be WIDTHxHEIGHT")
	}
	return w, h, nil
}

// --- handlers ---

// handleGenerate serves POST /v1/images/generations (OpenAI-compatible).
func (h *Handler) handleGenerate(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var req openAIImageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	params, err := req.toParams()
	if err != nil {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	gen, err := h.svc.Generate(c.Request.Context(), p.TenantID, params)
	if err != nil {
		writeGenerateError(c, err)
		return
	}

	format := req.ResponseFormat
	if format == "" {
		format = "b64_json"
	}
	data := make([]openAIImage, 0, len(gen.Assets))
	for _, a := range gen.Assets {
		item := openAIImage{
			RevisedPrompt: gen.RevisedPrompt,
			Seed:          a.Seed, Width: a.Width, Height: a.Height, Steps: a.Steps,
		}
		switch format {
		case "url":
			item.URL = "/api/v1/images/assets/" + a.ID
		default:
			// Re-read bytes for b64 (already persisted once).
			_, raw, err := h.svc.GetAsset(c.Request.Context(), p.TenantID, a.ID)
			if err != nil {
				writeGenerateError(c, err)
				return
			}
			item.B64JSON = base64.StdEncoding.EncodeToString(raw)
		}
		data = append(data, item)
	}

	response.WriteJSON(c, http.StatusOK, openAIImageResponse{Created: time.Now().Unix(), Data: data})
}

func (h *Handler) handleListAssets(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	assets, err := h.svc.ListAssets(c.Request.Context(), p.TenantID, parseLimit(c.Query("limit"), 50))
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, gin.H{"data": assets})
}

func (h *Handler) handleGetAsset(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	_, raw, err := h.svc.GetAsset(c.Request.Context(), p.TenantID, c.Param("id"))
	if err != nil {
		writeResourceError(c, err)
		return
	}
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Data(http.StatusOK, "image/png", raw)
}

func (h *Handler) handleCreateJob(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	var req openAIImageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	params, err := req.toParams()
	if err != nil {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	job, err := h.svc.CreateJob(c.Request.Context(), p.TenantID, params)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		if errors.Is(err, ErrModelNotFound) {
			response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", err.Error())
			return
		}
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusAccepted, job)
}

func (h *Handler) handleListJobs(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	jobs, err := h.svc.ListJobs(c.Request.Context(), p.TenantID, parseLimit(c.Query("limit"), 50))
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, gin.H{"data": jobs})
}

func (h *Handler) handleGetJob(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	job, err := h.svc.GetJob(c.Request.Context(), p.TenantID, c.Param("id"))
	if err != nil {
		writeResourceError(c, err)
		return
	}
	response.WriteJSON(c, http.StatusOK, job)
}

// handleJobEvents streams a job's progress over SSE until it reaches a terminal
// status. It polls the job row (the runner writes progress_step there), so no
// shared in-process state is needed and any API replica can serve the stream.
func (h *Handler) handleJobEvents(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")

	job, err := h.svc.GetJob(c.Request.Context(), p.TenantID, id)
	if err != nil {
		writeResourceError(c, err)
		return
	}
	sse, err := newSSEWriter(c.Writer)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	ctx := c.Request.Context()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := sse.send(job); err != nil {
			return
		}
		if isTerminalJob(job.Status) {
			sse.done()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		job, err = h.svc.GetJob(ctx, p.TenantID, id)
		if err != nil {
			_ = sse.sendError(err.Error())
			return
		}
	}
}

// --- error mapping ---

func writeGenerateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrModelNotFound):
		response.WriteOpenAIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", err.Error())
	default:
		response.WriteOpenAIError(c, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func writeResourceError(c *gin.Context, err error) {
	if errors.Is(err, ErrNotFound) {
		response.WriteAPIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "not found")
		return
	}
	response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}

func parseLimit(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		return def
	}
	return n
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func floatOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func int64Or(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}
