package session

import (
	"sync"
	"time"
)

// Role constants for internal canonical message format.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// Message in internal canonical format — protocol-agnostic.
type Message struct {
	Role        string     `json:"role"`
	Content     string     `json:"content,omitempty"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	ToolResult  string     `json:"tool_result,omitempty"`
	IsError     bool       `json:"is_error,omitempty"`
}

// ToolCall represents a tool invocation from the assistant.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters"` // JSON Schema string
}

// Session holds conversation state for one user.
type Session struct {
	mu       sync.RWMutex
	ID       string    `json:"id"`
	Messages []Message `json:"messages"`
	Metadata map[string]string

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Token budget (8K by default).
	MaxTokens int `json:"max_tokens"`

	// System prompt for this session.
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// NewSession creates a new session with defaults.
func NewSession(id string, maxTokens int) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		Messages:  make([]Message, 0),
		Metadata:  make(map[string]string),
		CreatedAt: now,
		UpdatedAt: now,
		MaxTokens: maxTokens,
	}
}

// AddMessage appends a message to the conversation history.
func (s *Session) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
}

// SetSystemPrompt updates the system prompt.
func (s *Session) SetSystemPrompt(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SystemPrompt = prompt
}

// GetMessages returns a copy of all messages.
func (s *Session) GetMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// ID returns the session ID.
func (s *Session) GetID() string { return s.ID }

// EstimatedTokens returns a rough estimate of token count.
// Go does a character-based estimate; Python worker gives exact count.
func (s *Session) EstimatedTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, m := range s.Messages {
		total += len(m.Content) / 4 // rough: ~4 chars per token
		total += len(m.ToolResult) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Arguments) / 4
			total += 10 // overhead per tool call
		}
		total += 4 // message overhead
	}
	return total
}
