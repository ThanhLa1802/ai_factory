package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrSessionNotFound means no session with that id exists for the tenant.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionForbidden means the session id is already claimed by another tenant.
	ErrSessionForbidden = errors.New("session belongs to another tenant")
)

// Store persists sessions and their messages. A nil store keeps the Manager
// purely in-memory (tests, or when the DB is unavailable).
type Store interface {
	// UpsertSession inserts or updates the session row (metadata only).
	UpsertSession(ctx context.Context, s *Session) error
	// LoadSession fetches a session + its messages by id, scoped to the tenant.
	LoadSession(ctx context.Context, id, tenantID string) (*Session, error)
	// AppendMessage inserts one message for a session at the given seq.
	AppendMessage(ctx context.Context, sessionID string, seq int, m Message) error
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
// is ignored rather than claimed.
func (p *PGStore) UpsertSession(ctx context.Context, s *Session) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, model, system_prompt, max_tokens)
		VALUES ($1, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6::int)
		ON CONFLICT (id) DO UPDATE SET
			model         = EXCLUDED.model,
			system_prompt = EXCLUDED.system_prompt,
			max_tokens    = EXCLUDED.max_tokens,
			updated_at    = now()
		WHERE sessions.tenant_id = EXCLUDED.tenant_id`,
		s.ID, s.TenantID, s.UserID, s.Model, s.SystemPrompt, s.MaxTokens)
	return err
}

// LoadSession reconstructs a Session from the sessions + messages tables.
func (p *PGStore) LoadSession(ctx context.Context, id, tenantID string) (*Session, error) {
	var (
		s      Session
		userID *string // nullable
	)
	err := p.pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, model, system_prompt, max_tokens, created_at, updated_at
		FROM sessions WHERE id = $1 AND tenant_id = $2`, id, tenantID).
		Scan(&s.TenantID, &userID, &s.Model, &s.SystemPrompt, &s.MaxTokens, &s.CreatedAt, &s.UpdatedAt)
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
