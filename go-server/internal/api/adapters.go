package api

import (
	"encoding/json"
	"fmt"

	"github.com/ai-factory/go-server/internal/session"
)

// ==========================================================================
// Internal Canonical Format Adapters
// ==========================================================================

// --------------------------------------------------------------------------
// OpenAI Chat Completions API → Internal
// --------------------------------------------------------------------------

// OpenAIRequest is the OpenAI /v1/chat/completions request body.
type OpenAIRequest struct {
	Model     string          `json:"model"`
	Messages  []OpenAIMessage  `json:"messages"`
	Stream    bool            `json:"stream,omitempty"`
	MaxTokens int32           `json:"max_tokens,omitempty"`
	Tools     []OpenAITool    `json:"tools,omitempty"`
}

// OpenAIMessage is a single message in OpenAI format.
type OpenAIMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// OpenAIToolCall is a tool call from the assistant.
type OpenAIToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function OpenAIFunction   `json:"function"`
}

// OpenAIFunction holds function name and arguments.
type OpenAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool defines a tool available to the model.
type OpenAITool struct {
	Type     string              `json:"type"`
	Function OpenAIFunctionDef   `json:"function"`
}

// OpenAIFunctionDef holds the function definition.
type OpenAIFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// OpenAIToInternal converts an OpenAI request to internal messages.
func OpenAIToInternal(req *OpenAIRequest) ([]session.Message, string, error) {
	msgs := make([]session.Message, 0, len(req.Messages))
	var systemPrompt string

	for _, om := range req.Messages {
		msg := session.Message{
			Role:    om.Role,
			Content: om.Content,
		}

		// Extract system prompt from system message
		if om.Role == session.RoleSystem && om.Content != "" {
			systemPrompt = om.Content
			continue // system messages are not part of conversation history
		}

		// Tool calls
		for _, tc := range om.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, session.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		// Tool result
		if om.Role == session.RoleTool {
			msg.ToolCallID = om.ToolCallID
			msg.ToolResult = om.Content
		}

		msgs = append(msgs, msg)
	}

	return msgs, systemPrompt, nil
}

// ValidateOpenAIRequest validates required fields.
func ValidateOpenAIRequest(req *OpenAIRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	return nil
}
