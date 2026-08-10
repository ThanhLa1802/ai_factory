package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/google/uuid"
)

// Package api cung cấp HTTP handlers, SSE streaming, protocol adapters và
// serve UI tĩnh từ thư mục ui/ (không nhúng HTML vào binary).
// Package comment đặt ở đây vì các file UI đã tách thành file HTML độc lập.

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	sessionMgr *session.Manager
	loop       *agent.Loop
	uiDir      string // thư mục chứa UI tĩnh (chat.html, concepts.html)
}

// NewHandler creates a new HTTP handler.
func NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string) *Handler {
	return &Handler{
		sessionMgr: sessionMgr,
		loop:       loop,
		uiDir:      uiDir,
	}
}

// RegisterRoutes registers all HTTP routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/messages", h.handleAnthropicMessages)
	mux.HandleFunc("/v1/chat/completions", h.handleOpenAIChatCompletions)
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/v1/sessions/", h.handleSessions)

	// Static UI for testing
	mux.HandleFunc("/", h.handleUI)
	// Technical concepts documentation
	mux.HandleFunc("/concepts", h.handleConcepts)
}

// ==========================================================================
// Anthropic /v1/messages
// ==========================================================================

func (h *Handler) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := ValidateAnthropicRequest(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Get or create session
	sessionID := r.Header.Get("x-session-id")
	if sessionID == "" {
		sessionID = session.NewSessionID()
	}
	sess := h.sessionMgr.GetOrCreate(sessionID)

	// Convert to internal format
	msgs, systemPrompt, err := AnthropicToInternal(&req)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if systemPrompt != "" {
		sess.SetSystemPrompt(systemPrompt)
	}

	params := inference.DefaultSamplingParams()
	if req.MaxTokens > 0 {
		params.MaxTokens = req.MaxTokens
	}

	// Context with cancel propagation on client disconnect
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-r.Context().Done()
		log.Printf("[api] Client disconnected for session %s", sessionID)
		cancel()
	}()

	if req.Stream {
		h.handleAnthropicStream(ctx, w, sess, msgs, params, sessionID, req.Model)
	} else {
		h.handleAnthropicNonStream(ctx, w, sess, msgs, params, sessionID, req.Model)
	}
}

func (h *Handler) handleAnthropicStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, sessionID, modelID string) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	messageID := "msg_" + uuid.New().String()[:8]
	w.Header().Set("x-session-id", sessionID)

	// Send message_start
	fmt.Fprintf(sse.w, "data: %s\n\n", mustMarshal(map[string]interface{}{
		"type":    "message_start",
		"message": map[string]string{"id": messageID, "model": modelID},
	}))
	sse.flusher.Flush()

	var (
		totalTokens   int32
		promptTokens  int32
		stopReason    string
		finishReason  string
	)

	for _, userMsg := range msgs {
		// Use streaming loop — tokens arrive one by one (batch scheduler handles concurrency)
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		for event := range events {
			switch event.Type {
			case agent.LoopEventToken:
				// Real token from gRPC → SSE immediately
				sse.SendToken(event.Token)
				totalTokens++

			case agent.LoopEventToolUse:
				if event.ToolCall != nil {
					sse.SendToolUse(event.ToolCall.ID, event.ToolCall.Name, event.ToolCall.Arguments)
				}

			case agent.LoopEventToolResult:
				// Tool results are sent to model in next iteration, not to client
				// (they'll be part of the next assistant response)

			case agent.LoopEventFinal:
				stopReason = event.StopReason
				finishReason = event.FinishReason
				if event.Usage != nil {
					promptTokens = event.Usage.PromptTokens
				}

			case agent.LoopEventError:
				sse.SendError(event.Err.Error())
				return
			}
		}

	}

	// Send final [DONE]
	usageMap := map[string]int32{
		"input_tokens":  promptTokens,
		"output_tokens": totalTokens,
	}
	sse.SendDone(stopReason, finishReason, usageMap)
}

func (h *Handler) handleAnthropicNonStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, sessionID, modelID string) {
	w.Header().Set("x-session-id", sessionID)

	var (
		contentBlocks []AnthropicContent
		stopReason    string
		usage         *inference.Usage
	)

	for _, userMsg := range msgs {
		events := h.loop.RunStreaming(ctx, sess, userMsg, params)

		var assistantContent string
		var toolCalls []session.ToolCall

		for event := range events {
			switch event.Type {
			case agent.LoopEventToken:
				assistantContent += event.Token

			case agent.LoopEventToolUse:
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, *event.ToolCall)
				}

			case agent.LoopEventFinal:
				stopReason = event.StopReason
				usage = event.Usage

			case agent.LoopEventError:
				writeAnthropicError(w, http.StatusInternalServerError, "internal_error", event.Err.Error())
				return
			}
		}

		// Build Anthropic content blocks from collected response
		if assistantContent != "" {
			contentBlocks = append(contentBlocks, AnthropicContent{
				Type: "text",
				Text: assistantContent,
			})
		}
		for _, tc := range toolCalls {
			var input json.RawMessage
			json.Unmarshal([]byte(tc.Arguments), &input)
			contentBlocks = append(contentBlocks, AnthropicContent{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}

	}

	stopMap := map[string]string{
		"STOP_END_TURN":  "end_turn",
		"STOP_MAX_TOKENS": "max_tokens",
		"STOP_TOOL_USE":  "tool_use",
	}

	resp := map[string]interface{}{
		"id":      "msg_" + uuid.New().String()[:8],
		"type":    "message",
		"role":    "assistant",
		"content": contentBlocks,
		"stop_reason": func() string {
			if s, ok := stopMap[stopReason]; ok {
				return s
			}
			return "end_turn"
		}(),
		"model": modelID,
	}
	if usage != nil {
		resp["usage"] = map[string]int32{
			"input_tokens":  usage.PromptTokens,
			"output_tokens": usage.CompletionTokens,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ==========================================================================
// OpenAI /v1/chat/completions
// ==========================================================================

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

	sessionID := r.Header.Get("x-session-id")
	if sessionID == "" {
		sessionID = session.NewSessionID()
	}
	sess := h.sessionMgr.GetOrCreate(sessionID)

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
		h.handleOpenAIStream(ctx, w, sess, msgs, params, req.Model)
	} else {
		h.handleOpenAINonStream(ctx, w, sess, msgs, params, req.Model)
	}
}

func (h *Handler) handleOpenAIStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, modelID string) {
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
				sse.SendError(event.Err.Error())
				return
			}
		}

	}

	fmt.Fprintf(sse.w, "data: [DONE]\n\n")
	sse.flusher.Flush()
}

func (h *Handler) handleOpenAINonStream(ctx context.Context, w http.ResponseWriter, sess *session.Session, msgs []session.Message, params inference.SamplingParams, modelID string) {
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
			case agent.LoopEventError:
				writeOpenAIError(w, http.StatusInternalServerError, "internal_error", event.Err.Error())
				return
			}
		}

	}

	resp := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:8],
		"object":  "chat.completion",
		"model":   modelID,
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

func (h *Handler) handleSessions(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		sessionID := parts[0]
		sess := h.sessionMgr.Get(sessionID)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         sess.GetID(),
			"messages":   sess.GetMessages(),
			"created_at": sess.CreatedAt,
			"updated_at": sess.UpdatedAt,
		})
		return
	}

	if r.Method == http.MethodDelete && len(parts) == 1 && parts[0] != "" {
		h.sessionMgr.Delete(parts[0])
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
}

func (h *Handler) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/ui" && r.URL.Path != "/ui/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.uiDir, "chat.html"))
}

func (h *Handler) handleConcepts(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(h.uiDir, "concepts.html"))
}

// ==========================================================================
// Helpers
// ==========================================================================

func writeAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}

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

func mustMarshal(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}
