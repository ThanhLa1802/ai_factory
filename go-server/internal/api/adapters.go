package api

import (
	"encoding/json"
	"fmt"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/session"
)

// ==========================================================================
// Internal Canonical Format Adapters
// ==========================================================================
// Convert Anthropic Messages API and OpenAI Chat Completions API
// into the internal session.Message format, and back.
// ==========================================================================

// --------------------------------------------------------------------------
// Anthropic Messages API → Internal
// --------------------------------------------------------------------------

// AnthropicRequest is the Anthropic /v1/messages request body.
type AnthropicRequest struct {
	Model     string            `json:"model"`
	Messages  []AnthropicMessage `json:"messages"`
	System    json.RawMessage   `json:"system,omitempty"` // string or []content block
	MaxTokens int32             `json:"max_tokens"`
	Stream    bool              `json:"stream,omitempty"`
	Tools     []AnthropicTool   `json:"tools,omitempty"`
}

// AnthropicMessage is a single message in Anthropic format.
type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

// AnthropicContent is a content block: text, tool_use, or tool_result.
type AnthropicContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`         // tool_use / tool_result
	Name       string          `json:"name,omitempty"`        // tool_use
	Input      json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID  string          `json:"tool_use_id,omitempty"` // tool_result
	Content    string          `json:"content,omitempty"`     // tool_result (string or content blocks)
	IsError    bool            `json:"is_error,omitempty"`
}

// AnthropicTool defines a tool in Anthropic format.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// AnthropicToInternal converts an Anthropic request to internal messages + system prompt.
func AnthropicToInternal(req *AnthropicRequest) ([]session.Message, string, error) {
	// System prompt — can be a string or an array of content blocks
	var systemPrompt string
	if len(req.System) > 0 {
		// Try string first
		var sysStr string
		if json.Unmarshal(req.System, &sysStr) == nil {
			systemPrompt = sysStr
		} else {
			// Try array of content blocks
			var sysBlocks []AnthropicContent
			if json.Unmarshal(req.System, &sysBlocks) == nil {
				for _, c := range sysBlocks {
					if c.Type == "text" {
						systemPrompt += c.Text
					}
				}
			}
		}
	}

	// Messages
	msgs := make([]session.Message, 0, len(req.Messages))
	for _, am := range req.Messages {
		msg := session.Message{Role: am.Role}

		for _, block := range am.Content {
			switch block.Type {
			case "text":
				msg.Content += block.Text
			case "tool_use":
				argsJSON, _ := json.Marshal(block.Input)
				msg.ToolCalls = append(msg.ToolCalls, session.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: string(argsJSON),
				})
			case "tool_result":
				msg.Role = session.RoleTool
				msg.ToolCallID = block.ToolUseID
				msg.ToolResult = block.Content
				msg.IsError = block.IsError
			}
		}

		msgs = append(msgs, msg)
	}

	return msgs, systemPrompt, nil
}

// InternalToAnthropicContent converts internal assistant message to Anthropic content blocks.
func InternalToAnthropicContent(msg session.Message) []AnthropicContent {
	blocks := make([]AnthropicContent, 0)

	if msg.Content != "" {
		blocks = append(blocks, AnthropicContent{
			Type: "text",
			Text: msg.Content,
		})
	}

	for _, tc := range msg.ToolCalls {
		var input json.RawMessage
		if tc.Arguments != "" {
			json.Unmarshal([]byte(tc.Arguments), &input)
		}
		blocks = append(blocks, AnthropicContent{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: input,
		})
	}

	return blocks
}

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

// InternalToOpenAIChoice converts internal messages to OpenAI choice format.
func InternalToOpenAIChoice(msg session.Message) map[string]interface{} {
	choice := map[string]interface{}{
		"message": map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		},
	}

	if len(msg.ToolCalls) > 0 {
		openaiTCs := make([]map[string]interface{}, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			openaiTCs[i] = map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]string{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			}
		}
		choice["message"].(map[string]interface{})["tool_calls"] = openaiTCs
	}

	return choice
}

// ToolsToInternal converts Anthropic tools to internal tool definitions.
func ToolsToInternal(anthropicTools []AnthropicTool) []agent.ToolDefinition {
	defs := make([]agent.ToolDefinition, len(anthropicTools))
	for i, t := range anthropicTools {
		schema, _ := json.Marshal(t.InputSchema)
		defs[i] = agent.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  string(schema),
		}
	}
	return defs
}

// OpenAIToolsToInternal converts OpenAI tools to internal tool definitions.
func OpenAIToolsToInternal(openaiTools []OpenAITool) []agent.ToolDefinition {
	defs := make([]agent.ToolDefinition, len(openaiTools))
	for i, t := range openaiTools {
		schema, _ := json.Marshal(t.Function.Parameters)
		defs[i] = agent.ToolDefinition{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  string(schema),
		}
	}
	return defs
}

// ValidateAnthropicRequest validates required fields.
func ValidateAnthropicRequest(req *AnthropicRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	return nil
}

// ValidateOpenAIRequest validates required fields.
func ValidateOpenAIRequest(req *OpenAIRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	return nil
}
