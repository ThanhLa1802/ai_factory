package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	// LoadSession fetches a session + its messages by id, scoped to the tenant.
	LoadSession(ctx context.Context, id, tenantID string) (*Session, error)
	// SessionExists reports whether a session row with the given id exists under
	// ANY tenant (no tenant filter) — used to reject cross-tenant session-id
	// collisions on a cold cache.
	SessionExists(ctx context.Context, id string) (bool, error)
	// AppendMessage inserts one message for a session at the given seq.
	AppendMessage(ctx context.Context, sessionID string, seq int, m Message) error
	// ListSessions returns sidebar summaries for the tenant, newest first.
	ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error)
	// RenameSession sets the display title.
	RenameSession(ctx context.Context, id, tenantID, title string) error
	// DeleteSession removes a session and its messages.
	DeleteSession(ctx context.Context, id, tenantID string) error
}

// PGStore is a PostgreSQL-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a PGStore over the given pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// UpsertSession inserts the session on first use and refreshes mutable metadata
// (model, system prompt, token budget) on subsequent messages. tenant_id/user_id
// are never overwritten on conflict — a colliding session id from another tenant
// surfaces as ErrSessionForbidden rather than being silently claimed.
func (p *PGStore) UpsertSession(ctx context.Context, s *Session) error {
	ct, err := p.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, model, system_prompt, max_tokens, title)
		VALUES ($1, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6::int, $7)
		ON CONFLICT (id) DO UPDATE SET
			model         = EXCLUDED.model,
			system_prompt = EXCLUDED.system_prompt,
			max_tokens    = EXCLUDED.max_tokens,
			title         = EXCLUDED.title,
			updated_at    = now()
		WHERE sessions.tenant_id = EXCLUDED.tenant_id`,
		s.ID, s.TenantID, s.UserID, s.Model, s.SystemPrompt, s.MaxTokens, s.Title)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		// The conflict fired on a row whose tenant_id != EXCLUDED.tenant_id, so the
		// guarded UPDATE no-oped. The id belongs to another tenant — never silently
		// claim it (that would let this caller write into a foreign conversation).
		return ErrSessionForbidden
	}
	return nil
}

// SessionExists reports whether a session row with the given id exists under ANY
// tenant (no tenant filter). Used to tell "id never existed" apart from "id is
// already claimed by another tenant" so a cold cache never mints a shadow session.
func (p *PGStore) SessionExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// LoadSession reconstructs a Session from the sessions + messages tables.
func (p *PGStore) LoadSession(ctx context.Context, id, tenantID string) (*Session, error) {
	var (
		s      Session
		userID *string // nullable
	)
	err := p.pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, model, system_prompt, max_tokens, title, created_at, updated_at
		FROM sessions WHERE id = $1 AND tenant_id = $2`, id, tenantID).
		Scan(&s.TenantID, &userID, &s.Model, &s.SystemPrompt, &s.MaxTokens, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if userID != nil {
		s.UserID = *userID
	}
	s.ID = id
	s.Metadata = make(map[string]string)

	rows, err := p.pool.Query(ctx, `
		SELECT role, content, tool_calls, tool_call_id, tool_result, is_error
		FROM messages WHERE session_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			m         Message
			toolCalls []byte
		)
		if err := rows.Scan(&m.Role, &m.Content, &toolCalls, &m.ToolCallID, &m.ToolResult, &m.IsError); err != nil {
			return nil, err
		}
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &m.ToolCalls); err != nil {
				return nil, err
			}
		}
		s.Messages = append(s.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resume the sequence counter and mark the row as already persisted so the
	// next AddMessage skips the redundant upsert.
	s.seq = len(s.Messages)
	s.persisted = true
	return &s, nil
}

// AppendMessage inserts one message. tool_calls is marshalled to JSON text and
// cast to JSONB server-side (pgx sends a Go string as text/unknown, not jsonb).
func (p *PGStore) AppendMessage(ctx context.Context, sessionID string, seq int, m Message) error {
	toolCalls := "[]"
	if len(m.ToolCalls) > 0 {
		if b, err := json.Marshal(m.ToolCalls); err == nil {
			toolCalls = string(b)
		}
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO messages (session_id, seq, role, content, tool_calls, tool_call_id, tool_result, is_error)
		VALUES ($1, $2::int, $3, $4, $5::jsonb, $6, $7, $8)`,
		sessionID, seq, m.Role, m.Content, toolCalls, m.ToolCallID, m.ToolResult, m.IsError)
	return err
}

// ListSessions returns sidebar summaries for the tenant, newest first. The title
// falls back to the first user message so legacy rows (empty title) still show
// something meaningful.
func (p *PGStore) ListSessions(ctx context.Context, tenantID string) ([]SessionSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT s.id,
		       COALESCE(NULLIF(s.title, ''),
		                (SELECT m.content FROM messages m
		                 WHERE m.session_id = s.id AND m.role = 'user'
		                 ORDER BY m.seq ASC LIMIT 1),
		                '') AS title,
		       s.model, s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)::int AS message_count
		FROM sessions s
		WHERE s.tenant_id = $1::uuid
		ORDER BY s.updated_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSummary{}
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Model, &s.CreatedAt, &s.UpdatedAt, &s.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RenameSession sets the display title, scoped to the tenant.
func (p *PGStore) RenameSession(ctx context.Context, id, tenantID, title string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE sessions SET title = $3, updated_at = now()
		WHERE id = $1 AND tenant_id = $2::uuid`, id, tenantID, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteSession removes a session (messages cascade), scoped to the tenant.
func (p *PGStore) DeleteSession(ctx context.Context, id, tenantID string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND tenant_id = $2::uuid`, id, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}
