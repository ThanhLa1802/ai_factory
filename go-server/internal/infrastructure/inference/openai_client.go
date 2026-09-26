package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAIClient calls an OpenAI-compatible upstream (vLLM, llama-server, …)
// directly over HTTP, bypassing the gRPC Python worker. It satisfies the same
// TrySubmit seam as BatchScheduler, so the agentic loop is unchanged.
//
// Enabled with `inference.mode=openai` plus `inference.url`/`inference.model`
// (env AI_FACTORY_INFERENCE_MODE/_URL/_MODEL). The Python worker remains the
// backend for the self-written `transformers` engine.
type OpenAIClient struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOpenAIClient builds a direct client for an OpenAI-compatible server.
func NewOpenAIClient(baseURL, model string) *OpenAIClient {
	return &OpenAIClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http: &http.Client{
			// No overall timeout: generations stream for a long time. The
			// request context governs cancellation; only the response headers
			// get a deadline so a dead upstream fails fast.
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

// Close releases idle keep-alive connections (DI lifecycle).
func (c *OpenAIClient) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

// TrySubmit opens a streaming chat completion and returns a channel of engine
// events. A 429/503 from the upstream is reported as ErrOverloaded so the
// handler sheds load, mirroring BatchScheduler.
func (c *OpenAIClient) TrySubmit(ctx context.Context, req GenerateRequest) (<-chan GenerateEvent, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		return nil, ErrOverloaded
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	events := make(chan GenerateEvent, 100)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		c.stream(ctx, resp.Body, events)
	}()
	return events, nil
}

// ---------------------------------------------------------------------------
// Request body
// ---------------------------------------------------------------------------

func (c *OpenAIClient) buildBody(req GenerateRequest) ([]byte, error) {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.Messages {
		// tool results collapse onto the OpenAI tool role.
		if m.Role == "tool" {
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.ToolResult,
			})
			continue
		}
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				calls[i] = map[string]any{
					"id":       tc.ID,
					"type":     "function",
					"function": map[string]string{"name": tc.Name, "arguments": tc.Arguments},
				}
			}
			msg["tool_calls"] = calls
		}
		messages = append(messages, msg)
	}

	body := map[string]any{
		"model":    c.model,
		"messages": messages,
		"stream":   true,
		// Emit usage on the final chunk (llama.cpp + vLLM require this).
		"stream_options": map[string]bool{"include_usage": true},
	}
	sp := req.SamplingParams
	if sp.MaxTokens > 0 {
		body["max_tokens"] = sp.MaxTokens
	}
	if sp.Temperature > 0 {
		body["temperature"] = sp.Temperature
	}
	if sp.TopP > 0 {
		body["top_p"] = sp.TopP
	}
	if sp.TopK > 0 {
		body["top_k"] = sp.TopK
	}
	if len(sp.StopSequences) > 0 {
		body["stop"] = sp.StopSequences
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, td := range req.Tools {
			fn := map[string]any{"name": td.Name, "description": td.Description}
			if strings.TrimSpace(td.Parameters) != "" {
				fn["parameters"] = json.RawMessage(td.Parameters)
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	return json.Marshal(body)
}

// ---------------------------------------------------------------------------
// SSE stream → engine events
// ---------------------------------------------------------------------------

// openAIChunk is the subset of a streaming chat.completion.chunk we consume.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *OpenAIClient) stream(ctx context.Context, r io.Reader, out chan<- GenerateEvent) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	toolAcc := map[int]*ToolUseEvent{}
	var (
		stopReason   string
		finishReason string
		usage        *Usage
	)

	for scanner.Scan() {
		if ctx.Err() != nil {
			out <- GenerateEvent{Type: "final", StopReason: "STOP_CANCELLED", FinishReason: "cancelled"}
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // ignore malformed keep-alive / partial frames
		}
		if chunk.Usage != nil {
			usage = &Usage{
				PromptTokens:     int32(chunk.Usage.PromptTokens),
				CompletionTokens: int32(chunk.Usage.CompletionTokens),
				TotalTokens:      int32(chunk.Usage.TotalTokens),
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.ReasoningContent != "" {
			out <- GenerateEvent{Type: "reasoning", Token: choice.Delta.ReasoningContent}
		}
		if choice.Delta.Content != "" {
			out <- GenerateEvent{Type: "token", Token: choice.Delta.Content}
		}
		for _, tc := range choice.Delta.ToolCalls {
			slot := toolAcc[tc.Index]
			if slot == nil {
				slot = &ToolUseEvent{}
				toolAcc[tc.Index] = slot
			}
			if tc.ID != "" {
				slot.ID = tc.ID
			}
			if tc.Function.Name != "" {
				slot.Name = tc.Function.Name
			}
			slot.Arguments += tc.Function.Arguments
		}
		if choice.FinishReason != nil && finishReason == "" {
			finishReason = *choice.FinishReason
			stopReason = stopReasonFor(finishReason)
			if stopReason == "STOP_TOOL_USE" {
				for _, i := range sortedIndices(toolAcc) {
					out <- GenerateEvent{Type: "tool_use", ToolUse: toolAcc[i]}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		out <- GenerateEvent{Type: "final", StopReason: "STOP_ERROR", FinishReason: "error", Error: err.Error()}
		return
	}
	if ctx.Err() != nil {
		out <- GenerateEvent{Type: "final", StopReason: "STOP_CANCELLED", FinishReason: "cancelled"}
		return
	}
	if stopReason == "" {
		stopReason = "STOP_END_TURN"
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	out <- GenerateEvent{Type: "final", StopReason: stopReason, FinishReason: finishReason, Usage: usage}
}

// stopReasonFor maps an OpenAI finish_reason onto the worker's StopReason enum.
func stopReasonFor(finishReason string) string {
	switch finishReason {
	case "length":
		return "STOP_MAX_TOKENS"
	case "tool_calls", "function_call":
		return "STOP_TOOL_USE"
	default:
		return "STOP_END_TURN"
	}
}

func sortedIndices(m map[int]*ToolUseEvent) []int {
	idx := make([]int, 0, len(m))
	for i := range m {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	return idx
}
