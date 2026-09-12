package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/cache"
	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Package inference owns the chat completions HTTP surface (OpenAI protocol,
// SSE), the agentic loop, tools, and durable sessions.

// ResolvedDeployment is the inference service's own view of a routable
// deployment. Defined here so inference never imports services/serving
// (design §4.2 / D-P4-2).
type ResolvedDeployment struct {
	ID       string
	TenantID string
	Region   string
}

// DeploymentResolver resolves model name → READY deployment. Satisfied by an
// adapter over *serving.Service, wired in the composition root.
type DeploymentResolver interface {
	ResolveDeployment(ctx context.Context, tenantID, modelName string) (*ResolvedDeployment, error)
}

// UsageRecorder persists token usage per completed turn. Satisfied by *usage.Service.
type UsageRecorder interface {
	RecordUsage(ctx context.Context, tenantID, model string, promptTokens, completionTokens int) error
}

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	sessionMgr *Manager
	loop       *Loop
	uiDir      string // thư mục chứa UI tĩnh (index.html, chat.html, keys.html)
	auth       middleware.Authenticator
	resolver   DeploymentResolver
	usage      UsageRecorder
	limiter    cache.Limiter
	rpmLimit   int
	concLimit  int
}

// NewHandler creates a new HTTP handler.
func NewHandler(sessionMgr *Manager, loop *Loop, uiDir string, auth middleware.Authenticator, resolver DeploymentResolver, usage UsageRecorder, limiter cache.Limiter, rpmLimit, concLimit int) *Handler {
	return &Handler{
		sessionMgr: sessionMgr, loop: loop, uiDir: uiDir, auth: auth,
		resolver: resolver, usage: usage, limiter: limiter, rpmLimit: rpmLimit, concLimit: concLimit,
	}
}

// ==========================================================================
// OpenAI /v1/chat/completions
// ==========================================================================

// resolveForTenant resolves tenant+model → READY deployment, then applies RPM +
// concurrency limits (fail-open on Redis error). On success returns the
// deployment and a release func (for concurrency); on failure writes the error
// response and returns nil, nil, false.
func (h *Handler) resolveForTenant(ctx context.Context, c *gin.Context, tenantID, model string) (*ResolvedDeployment, func(), bool) {
	d, err := h.resolver.ResolveDeployment(ctx, tenantID, model)
	if err != nil {
		response.WriteOpenAIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "no READY deployment for model")
		return nil, nil, false
	}
	if !h.allow(ctx, "tenant:"+tenantID+":rpm", h.rpmLimit, time.Minute) {
		response.WriteOpenAIError(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "rate limit exceeded")
		return nil, nil, false
	}
	if !h.acquire(ctx, "deployment:"+d.ID+":concurrency", h.concLimit) {
		response.WriteOpenAIError(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "concurrency limit exceeded")
		return nil, nil, false
	}
	release := func() { _ = h.limiter.Release(context.Background(), "deployment:"+d.ID+":concurrency") }
	return d, release, true
}

func (h *Handler) allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	ok, err := h.limiter.Allow(ctx, key, limit, window)
	if err != nil {
		slog.Warn("rate limit allow error (fail-open)", "err", err)
		return true
	}
	return ok
}

func (h *Handler) acquire(ctx context.Context, key string, limit int) bool {
	ok, err := h.limiter.Acquire(ctx, key, limit)
	if err != nil {
		slog.Warn("rate limit acquire error (fail-open)", "err", err)
		return true
	}
	return ok
}

func (h *Handler) handleOpenAIChatCompletions(c *gin.Context) {
	var req OpenAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := ValidateOpenAIRequest(&req); err != nil {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	p, _ := middleware.PrincipalFromContext(c)
	d, release, ok := h.resolveForTenant(c.Request.Context(), c, p.TenantID, req.Model)
	if !ok {
		return
	}
	defer release()
	if ls, ok := c.Writer.(observability.RouteLabelSetter); ok {
		ls.SetRouteLabels(d.TenantID, d.ID, req.Model, d.Region)
	}

	sessionID := c.GetHeader("x-session-id")
	if sessionID == "" {
		sessionID = NewSessionID()
	}
	userID := p.UserID
	sess, err := h.sessionMgr.GetOrCreate(c.Request.Context(), sessionID, p.TenantID, userID)
	if err != nil {
		// A cross-tenant session-id collision must be indistinguishable from a
		// missing session — 404 never reveals that the id exists elsewhere.
		if errors.Is(err, ErrSessionForbidden) {
			response.WriteOpenAIError(c, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		response.WriteOpenAIError(c, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	sess.Model = req.Model

	msgs, systemPrompt, err := OpenAIToInternal(&req)
	if err != nil {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if systemPrompt != "" {
		sess.SetSystemPrompt(systemPrompt)
	}

	params := infra.DefaultSamplingParams()
	if req.MaxTokens > 0 {
		params.MaxTokens = req.MaxTokens
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	go func() {
		<-c.Request.Context().Done()
		cancel()
	}()

	if req.Stream {
		h.handleOpenAIStream(ctx, c, sess, msgs, params, req.Model, p.TenantID)
	} else {
		h.handleOpenAINonStream(ctx, c, sess, msgs, params, req.Model, p.TenantID)
	}
}

func (h *Handler) handleOpenAIStream(ctx context.Context, c *gin.Context, sess *Session, msgs []Message, params infra.SamplingParams, modelID, tenantID string) {
	sse, err := NewSSEWriter(c.Writer)
	if err != nil {
		response.WriteOpenAIError(c, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	completionID := "chatcmpl-" + uuid.New().String()[:8]
	created := int32(0)

	for _, userMsg := range msgs {
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		for event := range events {
			switch event.Type {
			case LoopEventToken:
				// OpenAI SSE format — each token is a chunk
				data, _ := json.Marshal(map[string]interface{}{
					"id":      completionID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   modelID,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]string{"content": event.Token},
						},
					},
				})
				fmt.Fprintf(sse.w, "data: %s\n\n", string(data))
				sse.flusher.Flush()

			case LoopEventReasoning:
				// Reasoning token — display-only; emit as delta.reasoning_content.
				data, _ := json.Marshal(map[string]interface{}{
					"id":      completionID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   modelID,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]string{"reasoning_content": event.Token},
						},
					},
				})
				fmt.Fprintf(sse.w, "data: %s\n\n", string(data))
				sse.flusher.Flush()

			case LoopEventToolUse:
				// OpenAI format for tool calls in stream
				if event.ToolCall != nil {
					data, _ := json.Marshal(map[string]interface{}{
						"id":      completionID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   modelID,
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]interface{}{
									"tool_calls": []map[string]interface{}{
										{
											"index":    0,
											"id":       event.ToolCall.ID,
											"type":     "function",
											"function": map[string]string{"name": event.ToolCall.Name, "arguments": event.ToolCall.Arguments},
										},
									},
								},
							},
						},
					})
					fmt.Fprintf(sse.w, "data: %s\n\n", string(data))
					sse.flusher.Flush()
				}

			case LoopEventFinal:
				// Usage metering: Prometheus counter + durable usage_events row.
				if event.Usage != nil {
					observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
					if h.usage != nil {
						if err := h.usage.RecordUsage(ctx, tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens)); err != nil {
							slog.Warn("record usage", "err", err)
						}
					}
				}
				// Send final chunk
				data, _ := json.Marshal(map[string]interface{}{
					"id":      completionID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   modelID,
					"choices": []map[string]interface{}{
						{
							"index":         0,
							"delta":         map[string]string{},
							"finish_reason": event.FinishReason,
						},
					},
				})
				fmt.Fprintf(sse.w, "data: %s\n\n", string(data))
				sse.flusher.Flush()

			case LoopEventError:
				// Overload is load shedding (backpressure): the worker queue is
				// saturated, so tell the client to back off. SSE already started,
				// so surface it as an error frame rather than an HTTP status.
				if errors.Is(event.Err, infra.ErrOverloaded) {
					observability.IncOverloaded(tenantID, modelID)
					sse.SendError("OVERLOADED: " + event.Err.Error())
					return
				}
				sse.SendError(event.Err.Error())
				return
			}
		}

	}

	fmt.Fprintf(sse.w, "data: [DONE]\n\n")
	sse.flusher.Flush()
}

func (h *Handler) handleOpenAINonStream(ctx context.Context, c *gin.Context, sess *Session, msgs []Message, params infra.SamplingParams, modelID, tenantID string) {
	var (
		content string
		usage   *infra.Usage
	)

	for _, userMsg := range msgs {
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		for event := range events {
			switch event.Type {
			case LoopEventToken:
				content += event.Token
			case LoopEventFinal:
				usage = event.Usage
				// Usage metering: Prometheus counter + durable usage_events row.
				if event.Usage != nil {
					observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
					if h.usage != nil {
						if err := h.usage.RecordUsage(ctx, tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens)); err != nil {
							slog.Warn("record usage", "err", err)
						}
					}
				}
			case LoopEventError:
				if errors.Is(event.Err, infra.ErrOverloaded) {
					observability.IncOverloaded(tenantID, modelID)
					response.WriteOpenAIError(c, http.StatusServiceUnavailable, "overloaded", event.Err.Error())
					return
				}
				response.WriteOpenAIError(c, http.StatusInternalServerError, "internal_error", event.Err.Error())
				return
			}
		}

	}

	resp := map[string]interface{}{
		"id":     "chatcmpl-" + uuid.New().String()[:8],
		"object": "chat.completion",
		"model":  modelID,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	}
	if usage != nil {
		resp["usage"] = map[string]int32{
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.TotalTokens,
		}
	}

	c.JSON(http.StatusOK, resp)
}

// ==========================================================================
// Health, Sessions, UI
// ==========================================================================

func (h *Handler) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"server": "ai-factory",
	})
}

func (h *Handler) handleListSessions(c *gin.Context) {
	p, ok := middleware.PrincipalFromContext(c)
	if !ok {
		response.WriteAPIError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing claims")
		return
	}
	list, err := h.sessionMgr.ListSessions(c.Request.Context(), p.TenantID, p.UserID)
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, list)
}

func (h *Handler) handleGetSession(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	sess, err := h.sessionMgr.GetPersisted(c.Request.Context(), id, p.TenantID, p.UserID)
	if errors.Is(err, ErrSessionNotFound) {
		response.WriteAPIError(c, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if err != nil {
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, gin.H{
		"id":         sess.ID,
		"title":      sess.Title,
		"model":      sess.Model,
		"messages":   sess.Messages,
		"created_at": sess.CreatedAt,
		"updated_at": sess.UpdatedAt,
	})
}

func (h *Handler) handleRenameSession(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Title == "" {
		response.WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "title required")
		return
	}
	if err := h.sessionMgr.RenameSession(c.Request.Context(), id, p.TenantID, p.UserID, req.Title); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			response.WriteAPIError(c, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	response.WriteJSON(c, http.StatusOK, gin.H{"id": id, "title": req.Title})
}

func (h *Handler) handleDeleteSession(c *gin.Context) {
	p, _ := middleware.PrincipalFromContext(c)
	id := c.Param("id")
	if err := h.sessionMgr.DeleteSession(c.Request.Context(), id, p.TenantID, p.UserID); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			response.WriteAPIError(c, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		response.WriteAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) handleUI(c *gin.Context) {
	var file string
	switch c.Request.URL.Path {
	case "/":
		file = "index.html"
	case "/chat":
		file = "chat.html"
	case "/keys":
		file = "keys.html"
	default:
		c.Status(http.StatusNotFound)
		return
	}
	c.File(filepath.Join(h.uiDir, file))
}

func (h *Handler) handleConcepts(c *gin.Context) {
	c.File(filepath.Join(h.uiDir, "concepts.html"))
}
