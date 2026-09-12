package session

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrSessionNotFound means no session with that id exists for the tenant.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionForbidden means the session id is already claimed by another tenant.
	ErrSessionForbidden = errors.New("session belongs to another tenant")
)

// SessionSummary is a lightweight row for the chat sidebar list.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// Store persists sessions and their messages. A nil store keeps the Manager
// purely in-memory (tests, or when the DB is unavailable).
type Store interface {
	// UpsertSession inserts or updates the session row (metadata only).
	UpsertSession(ctx context.Context, s *Session) error
	// LoadSession fetches a session + its messages by id, scoped to the tenant
	// and user (empty userID matches sessions with a NULL user_id).
	LoadSession(ctx context.Context, id, tenantID, userID string) (*Session, error)
	// SessionExists reports whether a session row with the given id exists under
	// ANY tenant (no tenant filter) — used to reject cross-tenant session-id
	// collisions on a cold cache.
	SessionExists(ctx context.Context, id string) (bool, error)
	// AppendMessage inserts one message for a session at the given seq.
	AppendMessage(ctx context.Context, sessionID string, seq int, m Message) error
	// ListSessions returns sidebar summaries for the tenant + user, newest first.
	ListSessions(ctx context.Context, tenantID, userID string) ([]SessionSummary, error)
	// RenameSession sets the display title.
	RenameSession(ctx context.Context, id, tenantID, userID, title string) error
	// DeleteSession removes a session and its messages.
	DeleteSession(ctx context.Context, id, tenantID, userID string) error
}
