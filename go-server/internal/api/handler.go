package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/internal/ratelimit"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/google/uuid"
)

// Package api cung cấp HTTP handlers, SSE streaming, protocol adapters và
// serve UI tĩnh từ thư mục ui/ (không nhúng HTML vào binary).
// Package comment đặt ở đây vì các file UI đã tách thành file HTML độc lập.

// DeploymentResolver resolves model name → READY deployment. Satisfied by *controlplane.Service.
type DeploymentResolver interface {
	ResolveDeployment(ctx context.Context, tenantID, modelName string) (*controlplane.Deployment, error)
}

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	sessionMgr *session.Manager
	loop       *agent.Loop
	uiDir      string // thư mục chứa UI tĩnh (index.html, chat.html, keys.html)
	authSvc    *auth.Service
	secret     []byte
	resolver   DeploymentResolver
	limiter    ratelimit.Limiter
	rpmLimit   int
	concLimit  int
}

// NewHandler creates a new HTTP handler.
func NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string, authSvc *auth.Service, secret []byte, resolver DeploymentResolver, limiter ratelimit.Limiter, rpmLimit, concLimit int) *Handler {
	return &Handler{
		sessionMgr: sessionMgr, loop: loop, uiDir: uiDir, authSvc: authSvc, secret: secret,
		resolver: resolver, limiter: limiter, rpmLimit: rpmLimit, concLimit: concLimit,
	}
}

// RegisterRoutes registers all HTTP routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/v1/chat/completions", auth.InferenceAuth(h.secret, h.authSvc)(http.HandlerFunc(h.handleOpenAIChatCompletions)))
	mux.HandleFunc("/health", h.handleHealth)
	mux.Handle("GET /api/v1/sessions", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleListSessions)))
	mux.Handle("GET /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleGetSession)))
	mux.Handle("PATCH /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleRenameSession)))
	mux.Handle("DELETE /api/v1/sessions/", auth.RequireAuth(h.secret)(http.HandlerFunc(h.handleDeleteSession)))

	// Static UI for testing
	mux.HandleFunc("/", h.handleUI)
	// Technical concepts documentation
	mux.HandleFunc("/concepts", h.handleConcepts)
}

// ==========================================================================
// OpenAI /v1/chat/completions
// ==========================================================================

// resolveForTenant resolves tenant+model → READY deployment, then applies RPM +
// concurrency limits (fail-open on Redis error). On success returns the
// deployment and a release func (for concurrency); on failure writes the error
// response and returns nil, nil, false.
func (h *Handler) resolveForTenant(ctx context.Context, w http.ResponseWriter, tenantID, model string) (*controlplane.Deployment, func(), bool) {
	d, err := h.resolver.ResolveDeployment(ctx, tenantID, model)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "no READY deployment for model")
		return nil, nil, false
	}
	if !h.allow(ctx, "tenant:"+tenantID+":rpm", h.rpmLimit, time.Minute) {
		writeOpenAIError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "rate limit exceeded")
		return nil, nil, false
	}
	if !h.acquire(ctx, "deployment:"+d.ID+":concurrency", h.concLimit) {
		writeOpenAIError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "concurrency limit exceeded")
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

func (h *Handler) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := ValidateOpenAIRequest(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	tenantID, _ := auth.TenantIDFromContext(r.Context())
	d, release, ok := h.resolveForTenant(r.Context(), w, tenantID, req.Model)
	if !ok {
		return
	}
	defer release()
	if ls, ok := w.(observability.RouteLabelSetter); ok {
		ls.SetRouteLabels(d.TenantID, d.ID, req.Model, d.Region)
	}

	sessionID := r.Header.Get("x-session-id")
	if sessionID == "" {
		sessionID = session.NewSessionID()
	}
	userID := ""
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
		userID = claims.UserID
	}
	sess, err := h.sessionMgr.GetOrCreate(r.Context(), sessionID, tenantID, userID)
	if err != nil {
		writeOpenAIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	sess.Model = req.Model

	msgs, systemPrompt, err := OpenAIToInternal(&req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if systemPrompt != "" {
		sess.SetSystemPrompt(systemPrompt)
	}

	params := inference.DefaultSamplingParams()
	if req.MaxTokens > 0 {
		params.MaxTokens = req.MaxTokens
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	if req.Stream {
		h.handleOpenAIStream(ctx, w, sess, msgs, params, req.Model, tenantID)
	} else {
		h.handleOpenAINonStream(ctx, w, sess, msgs, params, req.Model, tenantID)
	}
}

func (h *Handler) handleOpenAIStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, modelID, tenantID string) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	completionID := "chatcmpl-" + uuid.New().String()[:8]
	created := int32(0)

	for _, userMsg := range msgs {
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		for event := range events {
			switch event.Type {
			case agent.LoopEventToken:
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

			case agent.LoopEventToolUse:
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

			case agent.LoopEventFinal:
				// Usage metering (A6): record prompt/completion tokens.
				if event.Usage != nil {
					observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
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

			case agent.LoopEventError:
				// Overload is load shedding (backpressure): the worker queue is
				// saturated, so tell the client to back off. SSE already started,
				// so surface it as an error frame rather than an HTTP status.
				if errors.Is(event.Err, inference.ErrOverloaded) {
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

func (h *Handler) handleOpenAINonStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, modelID, tenantID string) {
	var (
		content string
		usage   *inference.Usage
	)

	for _, userMsg := range msgs {
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		for event := range events {
			switch event.Type {
			case agent.LoopEventToken:
				content += event.Token
			case agent.LoopEventFinal:
				usage = event.Usage
				// Usage metering (A6): record prompt/completion tokens.
				if event.Usage != nil {
					observability.RecordTokenUsage(tenantID, modelID, int(event.Usage.PromptTokens), int(event.Usage.CompletionTokens))
				}
			case agent.LoopEventError:
				if errors.Is(event.Err, inference.ErrOverloaded) {
					observability.IncOverloaded(tenantID, modelID)
					writeOpenAIError(w, http.StatusServiceUnavailable, "overloaded", event.Err.Error())
					return
				}
				writeOpenAIError(w, http.StatusInternalServerError, "internal_error", event.Err.Error())
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ==========================================================================
// Health, Sessions, UI
// ==========================================================================

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"server": "ai-factory",
	})
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing claims")
		return
	}
	list, err := h.sessionMgr.ListSessions(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	sess, err := h.sessionMgr.GetPersisted(r.Context(), id, claims.TenantID)
	if errors.Is(err, session.ErrSessionNotFound) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         sess.ID,
		"title":      sess.Title,
		"model":      sess.Model,
		"messages":   sess.Messages,
		"created_at": sess.CreatedAt,
		"updated_at": sess.UpdatedAt,
	})
}

func (h *Handler) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "title required")
		return
	}
	if err := h.sessionMgr.RenameSession(r.Context(), id, claims.TenantID, req.Title); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "title": req.Title})
}

func (h *Handler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if err := h.sessionMgr.DeleteSession(r.Context(), id, claims.TenantID); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleUI(w http.ResponseWriter, r *http.Request) {
	var file string
	switch r.URL.Path {
	case "/":
		file = "index.html"
	case "/chat":
		file = "chat.html"
	case "/keys":
		file = "keys.html"
	default:
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.uiDir, file))
}

func (h *Handler) handleConcepts(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(h.uiDir, "concepts.html"))
}

// ==========================================================================
// Helpers
// ==========================================================================

func writeOpenAIError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"type":    typ,
			"message": msg,
			"code":    status,
		},
	})
}
