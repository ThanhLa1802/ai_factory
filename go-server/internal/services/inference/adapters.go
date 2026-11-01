package inference

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
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
	Role       string           `json:"role"`
	Content    OpenAIContent    `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// OpenAIContent accepts either a plain string (text-only requests) or an array
// of content parts (vision requests): [{type:"text",text:...},
// {type:"image_url",image_url:{url:"data:image/png;base64,..."}}].
type OpenAIContent struct {
	Text   string
	Images []MessageImage
}

// OpenAIContentPart is one element of an OpenAI multimodal content array.
type OpenAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *OpenAIImageURL `json:"image_url,omitempty"`
}

// OpenAIImageURL is the image_url content part payload.
type OpenAIImageURL struct {
	URL string `json:"url"`
}

// UnmarshalJSON decodes both content shapes. Image URLs must be inline base64
// data URLs; remote URLs are rejected (no server-side fetch → no SSRF surface).
func (c *OpenAIContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		c.Images = nil
		return nil
	}

	var parts []OpenAIContentPart
	if err := json.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("content must be a string or an array of content parts")
	}
	c.Text = ""
	c.Images = nil
	for _, p := range parts {
		switch p.Type {
		case "text":
			c.Text += p.Text
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				return fmt.Errorf("image_url.url is required")
			}
			mime, raw, err := parseImageDataURL(p.ImageURL.URL)
			if err != nil {
				return err
			}
			c.Images = append(c.Images, MessageImage{MimeType: mime, Data: raw})
		default:
			return fmt.Errorf("unsupported content part type %q", p.Type)
		}
	}
	return nil
}

// parseImageDataURL splits `data:<mime>;base64,<payload>` into its parts.
func parseImageDataURL(url string) (string, []byte, error) {
	if !strings.HasPrefix(url, "data:") {
		return "", nil, fmt.Errorf("image_url must be an inline data: URL (base64); remote URLs are not fetched")
	}
	meta, payload, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !ok {
		return "", nil, fmt.Errorf("malformed image data URL")
	}
	if !strings.HasSuffix(meta, ";base64") {
		return "", nil, fmt.Errorf("image data URL must be base64-encoded")
	}
	mime := strings.TrimSuffix(meta, ";base64")
	if mime != "image/png" && mime != "image/jpeg" {
		return "", nil, fmt.Errorf("unsupported image type %q (use image/png or image/jpeg)", mime)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decode image base64: %w", err)
	}
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("image is empty")
	}
	return mime, raw, nil
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
func OpenAIToInternal(req *OpenAIRequest) ([]Message, string, error) {
	msgs := make([]Message, 0, len(req.Messages))
	var systemPrompt string

	for _, om := range req.Messages {
		msg := Message{
			Role:    om.Role,
			Content: om.Content.Text,
			Images:  om.Content.Images,
		}

		// Extract system prompt from system message
		if om.Role == RoleSystem && om.Content.Text != "" {
			systemPrompt = om.Content.Text
			continue // system messages are not part of conversation history
		}

		// Tool calls
		for _, tc := range om.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		// Tool result
		if om.Role == RoleTool {
			msg.ToolCallID = om.ToolCallID
			msg.ToolResult = om.Content.Text
		}

		msgs = append(msgs, msg)
	}

	return msgs, systemPrompt, nil
}

// OpenAIToolsToInternal converts tools declared by the client into the wire
// definition carried on GenerateRequest. Tools without a function name are
// dropped. Returns nil when the request declares no tools.
func OpenAIToolsToInternal(tools []OpenAITool) []infra.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	out := make([]infra.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if t.Function.Name == "" {
			continue
		}
		out = append(out, infra.ToolDefinition{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  string(t.Function.Parameters),
		})
	}
	return out
}

// ValidateOpenAIRequest validates required fields.
func ValidateOpenAIRequest(req *OpenAIRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	return nil
}
