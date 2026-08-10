package session

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

const (
	// DefaultMaxTokens for context window.
	DefaultMaxTokens = 8192 // 8K
	// DangerZoneBeforeTruncate — start truncating when at this % of max.
	DangerZoneBeforeTruncate = 0.90
)

// Manager handles multiple in-memory sessions.
// Concurrency is now managed by BatchScheduler instead of inference queue.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager creates a session manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// GetOrCreate returns an existing session or creates a new one.
func (m *Manager) GetOrCreate(sessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[sessionID]; ok {
		return s
	}

	s := NewSession(sessionID, DefaultMaxTokens)
	m.sessions[sessionID] = s
	return s
}

// Get returns a session by ID or nil.
func (m *Manager) Get(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// Delete removes a session.
func (m *Manager) Delete(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

// List returns all session IDs.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// NewSessionID generates a unique session ID.
func NewSessionID() string {
	return uuid.New().String()
}

// TruncateMessages trims old messages to fit within the token budget.
// Preserves system prompt + ensures tool_use/tool_result pairs stay intact.
func TruncateMessages(msgs []Message, maxTokens int) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	// Estimate tokens and truncate from the beginning.
	// Keep at least the last N messages.
	// Simple approach: keep most recent messages that fit within budget.
	estimated := 0
	keepFrom := len(msgs)

	// Estimate from the end backwards
	for i := len(msgs) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokens(msgs[i])
		if estimated+msgTokens > maxTokens && i < len(msgs)-1 {
			keepFrom = i + 1
			break
		}
		estimated += msgTokens
		if i == 0 {
			keepFrom = 0
		}
	}

	// Ensure we don't split tool_use / tool_result pairs.
	// If the first kept message is a tool_result, also keep its tool_use.
	if keepFrom < len(msgs) && keepFrom > 0 {
		if msgs[keepFrom].Role == RoleTool && msgs[keepFrom].ToolCallID != "" {
			// Find the matching tool_use from assistant
			for j := keepFrom - 1; j >= 0; j-- {
				for _, tc := range msgs[j].ToolCalls {
					if tc.ID == msgs[keepFrom].ToolCallID {
						keepFrom = j
						break
					}
				}
			}
		}
	}

	// If the last kept message is a tool_use with no result, also drop it
	// (model can't handle orphaned tool calls).
	if keepFrom < len(msgs) && len(msgs[len(msgs)-1].ToolCalls) > 0 {
		lastCall := msgs[len(msgs)-1].ToolCalls[len(msgs[len(msgs)-1].ToolCalls)-1]
		hasResult := false
		for k := len(msgs) - 1; k >= keepFrom; k-- {
			if msgs[k].Role == RoleTool && msgs[k].ToolCallID == lastCall.ID {
				hasResult = true
				break
			}
		}
		if !hasResult {
			// Remove the orphaned tool_use
			if len(msgs)-1 >= keepFrom {
				msgs = msgs[:len(msgs)-1]
			}
		}
	}

	return msgs[keepFrom:]
}

func estimateMessageTokens(msg Message) int {
	tokens := 4 // overhead
	tokens += len(msg.Content) / 4
	tokens += len(msg.ToolResult) / 4
	for _, tc := range msg.ToolCalls {
		tokens += len(tc.Name) / 4
		tokens += len(tc.Arguments) / 4
		tokens += 10
	}
	return tokens
}

// ValidateSessionID checks session ID format.
func ValidateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session_id is required")
	}
	if len(id) > 128 {
		return fmt.Errorf("session_id too long (max 128)")
	}
	return nil
}
