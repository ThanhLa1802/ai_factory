package inference

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	// DefaultMaxTokens for context window.
	DefaultMaxTokens = 32768 // 32K
	// DangerZoneBeforeTruncate — start truncating when at this % of max.
	DangerZoneBeforeTruncate = 0.90
	// defaultMaxSessions bounds the in-memory session cache. Eviction is safe:
	// the DB is the source of truth and a miss is lazily reloaded.
	defaultMaxSessions = 10000
)

// Manager handles multiple in-memory sessions backed by an optional Store.
// The in-memory cache is a bounded LRU guarded by a short critical section; DB
// I/O happens outside the lock, and concurrent loads of the same session are
// coalesced by singleflight.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*list.Element // id → *sessionEntry (LRU element)
	order    *list.List               // front = most recently used
	max      int
	store    Store
	loads    singleflight.Group
}

type sessionEntry struct {
	id   string
	sess *Session
}

// NewManager creates an in-memory-only session manager.
func NewManager() *Manager { return newManager(nil, defaultMaxSessions) }

// NewManagerWithStore creates a session manager that persists sessions through
// the given store (lazy-load on miss, write-through on AddMessage).
func NewManagerWithStore(store Store) *Manager { return newManager(store, defaultMaxSessions) }

func newManager(store Store, max int) *Manager {
	if max < 1 {
		max = defaultMaxSessions
	}
	return &Manager{
		sessions: make(map[string]*list.Element),
		order:    list.New(),
		max:      max,
		store:    store,
	}
}

// sessionAccessError rejects a session that belongs to another tenant or user.
func sessionAccessError(s *Session, tenantID, userID string) error {
	if s.TenantID != "" && s.TenantID != tenantID {
		return ErrSessionForbidden
	}
	if s.UserID != "" && s.UserID != userID {
		return ErrSessionForbidden
	}
	return nil
}

// lookup returns a cached session and marks it most-recently-used.
func (m *Manager) lookup(sessionID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.sessions[sessionID]
	if !ok {
		return nil, false
	}
	m.order.MoveToFront(el)
	return el.Value.(*sessionEntry).sess, true
}

// GetOrCreate returns an existing session (from memory or the store) or creates
// a new one bound to the tenant. Returns ErrSessionForbidden if the session id
// is already claimed by a different tenant. A store error degrades to a fresh
// in-memory session (fail-open) rather than failing the request.
func (m *Manager) GetOrCreate(ctx context.Context, sessionID, tenantID, userID string) (*Session, error) {
	// Fast path: a cached session, no DB and no global lock held across I/O.
	if s, ok := m.lookup(sessionID); ok {
		if err := sessionAccessError(s, tenantID, userID); err != nil {
			return nil, err
		}
		return s, nil
	}

	// Slow path: coalesce concurrent loads of the same (session, owner) pair.
	key := sessionID + "\x00" + tenantID + "\x00" + userID
	v, err, _ := m.loads.Do(key, func() (any, error) {
		// Another goroutine may have populated the cache while we queued.
		if s, ok := m.lookup(sessionID); ok {
			if err := sessionAccessError(s, tenantID, userID); err != nil {
				return nil, err
			}
			return s, nil
		}
		return m.loadOrCreate(ctx, sessionID, tenantID, userID)
	})
	if err != nil {
		return nil, err
	}
	s := v.(*Session)
	if err := sessionAccessError(s, tenantID, userID); err != nil {
		return nil, err
	}
	return s, nil
}

// loadOrCreate hits the store (or builds a fresh session) without holding the
// manager lock, then inserts into the bounded cache.
func (m *Manager) loadOrCreate(ctx context.Context, sessionID, tenantID, userID string) (*Session, error) {
	if m.store != nil {
		s, err := m.store.LoadSession(ctx, sessionID, tenantID, userID)
		if err == nil {
			s.store = m.store
			return m.cache(s), nil
		}
		if !errors.Is(err, ErrSessionNotFound) {
			slog.Error("load session", "session_id", sessionID, "err", err)
		} else {
			// The id isn't visible to this tenant, but it may still exist under
			// another tenant's ownership. Distinguish "never existed" from
			// "claimed elsewhere" so a cold cache doesn't mint a shadow session
			// that would let this tenant write into a foreign conversation.
			exists, exErr := m.store.SessionExists(ctx, sessionID)
			if exErr != nil {
				slog.Error("session exists", "session_id", sessionID, "err", exErr)
			} else if exists {
				return nil, ErrSessionForbidden
			}
		}
	}

	s := NewSession(sessionID, DefaultMaxTokens)
	s.TenantID = tenantID
	s.UserID = userID
	s.store = m.store
	return m.cache(s), nil
}

// cache inserts a session and evicts the least-recently-used entry when over the
// cap. If the id is already cached it returns the existing session.
func (m *Manager) cache(s *Session) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.sessions[s.ID]; ok {
		m.order.MoveToFront(el)
		return el.Value.(*sessionEntry).sess
	}
	m.sessions[s.ID] = m.order.PushFront(&sessionEntry{id: s.ID, sess: s})
	for m.order.Len() > m.max {
		back := m.order.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*sessionEntry)
		m.order.Remove(back)
		delete(m.sessions, entry.id)
	}
	return s
}

// Get returns a session by ID or nil.
func (m *Manager) Get(sessionID string) *Session {
	s, _ := m.lookup(sessionID)
	return s
}

// Delete removes a session from the in-memory cache.
func (m *Manager) Delete(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.sessions[sessionID]; ok {
		m.order.Remove(el)
		delete(m.sessions, sessionID)
	}
}

// List returns all cached session IDs.
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// ListSessions returns sidebar summaries for the tenant + user (store-backed).
func (m *Manager) ListSessions(ctx context.Context, tenantID, userID string) ([]SessionSummary, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	return m.store.ListSessions(ctx, tenantID, userID)
}

// GetPersisted loads a session + messages from the store, scoped to the tenant
// + user. Returns ErrSessionNotFound when absent. The returned session is wired
// to the store so a later AddMessage persists rather than re-upserting.
func (m *Manager) GetPersisted(ctx context.Context, id, tenantID, userID string) (*Session, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	s, err := m.store.LoadSession(ctx, id, tenantID, userID)
	if err != nil {
		return nil, err
	}
	s.store = m.store
	return s, nil
}

// RenameSession renames a session by id, scoped to the tenant + user.
func (m *Manager) RenameSession(ctx context.Context, id, tenantID, userID, title string) error {
	if m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	return m.store.RenameSession(ctx, id, tenantID, userID, title)
}

// DeleteSession removes a session from the store and the in-memory cache.
func (m *Manager) DeleteSession(ctx context.Context, id, tenantID, userID string) error {
	if m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if err := m.store.DeleteSession(ctx, id, tenantID, userID); err != nil {
		return err
	}
	m.Delete(id)
	return nil
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
