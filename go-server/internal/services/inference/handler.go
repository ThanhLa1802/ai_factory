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

// ErrInsufficientCredits is returned by the billing gate when the tenant's
// available balance cannot cover the estimated cost of a request.
var ErrInsufficientCredits = errors.New("insufficient credits")

// BillingGate is the inference service's view of prepaid billing. Satisfied by
// an adapter over *billing.Service, wired in the composition root (the adapter
// translates the billing sentinel error). A nil gate disables billing.
type BillingGate interface {
	Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error)
	Settle(ctx context.Context, reservationID string, promptTokens, completionTokens int) error
	Release(ctx context.Context, reservationID string) error
}

// ErrQuotaExceeded is returned by the quota gate when a tenant has met or
// exceeded one of its usage quotas.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaGate is the inference service's view of tenant quota enforcement.
// Satisfied by an adapter over *usage.QuotaEnforcer, wired in the composition
// root (the adapter translates the usage sentinel error). A nil gate disables
// quota enforcement.
type QuotaGate interface {
	Check(ctx context.Context, tenantID string) error
}

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	sessionMgr *Manager
	loop       *Loop
	uiDir      string // thư mục chứa UI tĩnh (index.html, chat.html, keys.html)
	auth       middleware.Authenticator
	resolver   DeploymentResolver
	usage      UsageRecorder
	billing    BillingGate
	quota      QuotaGate
	limiter    cache.Limiter
	rpmLimit   int
	concLimit  int
}

// NewHandler creates a new HTTP handler.
func NewHandler(sessionMgr *Manager, loop *Loop, uiDir string, auth middleware.Authenticator, resolver DeploymentResolver, usage UsageRecorder, billing BillingGate, quota QuotaGate, limiter cache.Limiter, rpmLimit, concLimit int) *Handler {
	return &Handler{
		sessionMgr: sessionMgr, loop: loop, uiDir: uiDir, auth: auth,
		resolver: resolver, usage: usage, billing: billing, quota: quota, limiter: limiter, rpmLimit: rpmLimit, concLimit: concLimit,
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

// checkQuota enforces the tenant's usage quotas before any work is done. It
// returns ok=false (having written the error response) when an enforced quota
// is exceeded. Infrastructure errors fail open, like the rate limiter.
func (h *Handler) checkQuota(ctx context.Context, c *gin.Context, tenantID string) bool {
	if h.quota == nil {
		return true
	}
	if err := h.quota.Check(ctx, tenantID); err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			response.WriteOpenAIError(c, http.StatusTooManyRequests, "QUOTA_EXCEEDED", "tenant quota exceeded")
			return false
		}
		slog.Warn("quota check error (fail-open)", "err", err)
		return true
	}
	return true
}

// reserveBilling places a prepaid hold for the request. It returns ok=false
// (having written the error response) when billing rejects the request.
func (h *Handler) reserveBilling(ctx context.Context, c *gin.Context, tenantID, model string, msgs []Message, params infra.SamplingParams) (string, bool) {
	if h.billing == nil {
		return "", true
	}
	estInput := 0
	for _, m := range msgs {
		estInput += estimateMessageTokens(m)
	}
	id, err := h.billing.Reserve(ctx, tenantID, model, estInput, int(params.MaxTokens))
	if err != nil {
		if errors.Is(err, ErrInsufficientCredits) {
			response.WriteOpenAIError(c, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", "insufficient credits")
			return "", false
		}
		slog.Error("billing reserve", "err", err)
		response.WriteOpenAIError(c, http.StatusInternalServerError, "internal_error", "billing error")
		return "", false
	}
	return id, true
}

// settleBilling captures real usage against the hold on a detached context so a
// cancelled request still settles. Best-effort: failures are logged.
func (h *Handler) settleBilling(reservationID string, promptTokens, completionTokens int) {
	if h.billing == nil || reservationID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.billing.Settle(ctx, reservationID, promptTokens, completionTokens); err != nil {
		slog.Warn("billing settle", "err", err, "reservation", reservationID)
	}
}

// releaseBilling frees an unused hold (no charge). Best-effort.
func (h *Handler) releaseBilling(reservationID string) {
	if h.billing == nil || reservationID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.billing.Release(ctx, reservationID); err != nil {
		slog.Warn("billing release", "err", err, "reservation", reservationID)
	}
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

	if !h.checkQuota(c.Request.Context(), c, p.TenantID) {
		return
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

	// Only the final user turn is answered; everything before it is history.
	// Our own UIs keep history in the session and send just the new user
	// message, while a stateless client (e.g. a coding agent) resends the whole
	// conversation on every request — so a fresh session is seeded with those
	// prior turns instead of replaying each of them as its own generation.
	history, turn, ok := lastUserTurn(msgs)
	if !ok {
		response.WriteOpenAIError(c, http.StatusBadRequest, "invalid_request", "messages must contain a user message")
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	go func() {
		<-c.Request.Context().Done()
		cancel()
	}()

	if len(sess.GetMessages()) == 0 {
		for _, m := range history {
			sess.AddMessage(ctx, m)
		}
	}

	reservationID, ok := h.reserveBilling(ctx, c, p.TenantID, req.Model, msgs, params)
	if !ok {
		return
	}

	clientTools := OpenAIToolsToInternal(req.Tools)
	if req.Stream {
		h.handleOpenAIStream(ctx, c, sess, turn, params, clientTools, req.Model, p.TenantID, reservationID)
	} else {
		h.handleOpenAINonStream(ctx, c, sess, turn, params, clientTools, req.Model, p.TenantID, reservationID)
	}
}

// lastUserTurn splits a request's conversation into the history that precedes
// the final user turn and that turn itself. It reports ok=false when the
// request carries no user message.
func lastUserTurn(msgs []Message) (history []Message, turn Message, ok bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser {
			return msgs[:i], msgs[i], true
		}
	}
	return nil, Message{}, false
}

func (h *Handler) handleOpenAIStream(ctx context.Context, c *gin.Context, sess *Session, turn Message, params infra.SamplingParams, clientTools []infra.ToolDefinition, modelID, tenantID, reservationID string) {
	sse, err := NewSSEWriter(c.Writer)
	if err != nil {
		response.WriteOpenAIError(c, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	completionID := "chatcmpl-" + uuid.New().String()[:8]
	created := int32(0)

	promptTotal, completionTotal := 0, 0
	defer func() {
		if promptTotal == 0 && completionTotal == 0 {
			h.releaseBilling(reservationID)
			return
		}
		h.settleBilling(reservationID, promptTotal, completionTotal)
	}()

	events := h.loop.RunStreaming(ctx, sess, turn, params, clientTools)

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
				promptTotal += int(event.Usage.PromptTokens)
				completionTotal += int(event.Usage.CompletionTokens)
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

	fmt.Fprintf(sse.w, "data: [DONE]\n\n")
	sse.flusher.Flush()
}

func (h *Handler) handleOpenAINonStream(ctx context.Context, c *gin.Context, sess *Session, turn Message, params infra.SamplingParams, clientTools []infra.ToolDefinition, modelID, tenantID, reservationID string) {
	var (
		content      string
		usage        *infra.Usage
		toolCalls    []ToolCall
		finishReason = "stop"
	)

	promptTotal, completionTotal := 0, 0
	defer func() {
		if promptTotal == 0 && completionTotal == 0 {
			h.releaseBilling(reservationID)
			return
		}
		h.settleBilling(reservationID, promptTotal, completionTotal)
	}()

	events := h.loop.RunStreaming(ctx, sess, turn, params, clientTools)

	for event := range events {
		switch event.Type {
		case LoopEventToken:
			content += event.Token
		case LoopEventToolUse:
			if event.ToolCall != nil {
				toolCalls = append(toolCalls, *event.ToolCall)
			}
		case LoopEventFinal:
			usage = event.Usage
			if event.FinishReason != "" {
				finishReason = event.FinishReason
			}
			// Usage metering: Prometheus counter + durable usage_events row.
			if event.Usage != nil {
				promptTotal += int(event.Usage.PromptTokens)
				completionTotal += int(event.Usage.CompletionTokens)
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

	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = openAIToolCalls(toolCalls)
	}

	resp := map[string]interface{}{
		"id":     "chatcmpl-" + uuid.New().String()[:8],
		"object": "chat.completion",
		"model":  modelID,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
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

// openAIToolCalls renders internal tool calls in OpenAI response format.
func openAIToolCalls(calls []ToolCall) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(calls))
	for _, tc := range calls {
		out = append(out, map[string]interface{}{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]string{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			},
		})
	}
	return out
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
