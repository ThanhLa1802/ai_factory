package session

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormStore is a GORM-backed Store.
type GormStore struct {
	db *gorm.DB
}

// NewGormStore creates a GORMStore over the given GORM handle.
func NewGormStore(db *gorm.DB) *GormStore { return &GormStore{db: db} }

// UpsertSession inserts the session on first use and refreshes mutable metadata
// (model, system prompt, token budget, title) on subsequent messages.
// tenant_id/user_id are never overwritten on conflict — a colliding session id
// from another tenant surfaces as ErrSessionForbidden rather than being claimed.
func (s *GormStore) UpsertSession(ctx context.Context, sess *Session) error {
	row := sessionRow{
		ID: sess.ID, TenantID: sess.TenantID, UserID: nullableString(sess.UserID),
		Model: sess.Model, SystemPrompt: sess.SystemPrompt, MaxTokens: sess.MaxTokens, Title: sess.Title,
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"model":         sess.Model,
			"system_prompt": sess.SystemPrompt,
			"max_tokens":    sess.MaxTokens,
			"title":         sess.Title,
			"updated_at":    time.Now().UTC(),
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: "sessions", Name: "tenant_id"}, Value: sess.TenantID},
		}},
	}).Create(&row)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// The guarded UPDATE no-oped: the id belongs to another tenant.
		return ErrSessionForbidden
	}
	return nil
}

// SessionExists reports whether a session row with the given id exists under ANY
// tenant (no tenant filter).
func (s *GormStore) SessionExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.WithContext(ctx).
		Raw(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)`, id).
		Scan(&exists).Error
	if err != nil {
		return false, err
	}
	return exists, nil
}

// LoadSession reconstructs a Session from the sessions + messages tables,
// scoped to tenant AND user.
func (s *GormStore) LoadSession(ctx context.Context, id, tenantID, userID string) (*Session, error) {
	var row sessionRow
	err := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND user_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid", id, tenantID, userID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	var msgs []messageRow
	if err := s.db.WithContext(ctx).
		Where("session_id = ?", id).
		Order("seq").
		Find(&msgs).Error; err != nil {
		return nil, err
	}

	sess := &Session{
		ID:           id,
		TenantID:     row.TenantID,
		Model:        row.Model,
		SystemPrompt: row.SystemPrompt,
		MaxTokens:    row.MaxTokens,
		Title:        row.Title,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		Metadata:     make(map[string]string),
	}
	if row.UserID != nil {
		sess.UserID = *row.UserID
	}
	for _, m := range msgs {
		sess.Messages = append(sess.Messages, Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			ToolResult: m.ToolResult,
			IsError:    m.IsError,
		})
	}
	sess.seq = len(sess.Messages)
	sess.persisted = true
	return sess, nil
}

// AppendMessage inserts one message for a session at the given seq.
func (s *GormStore) AppendMessage(ctx context.Context, sessionID string, seq int, m Message) error {
	toolCalls := m.ToolCalls
	if toolCalls == nil {
		toolCalls = []ToolCall{}
	}
	row := messageRow{
		SessionID: sessionID, Seq: seq, Role: m.Role, Content: m.Content,
		ToolCalls: toolCalls, ToolCallID: m.ToolCallID, ToolResult: m.ToolResult, IsError: m.IsError,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

// ListSessions returns sidebar summaries for the tenant + user, newest first.
// The title falls back to the first user message so legacy rows still show one.
func (s *GormStore) ListSessions(ctx context.Context, tenantID, userID string) ([]SessionSummary, error) {
	var out []SessionSummary
	err := s.db.WithContext(ctx).Raw(`
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
		  AND s.user_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		ORDER BY s.updated_at DESC`, tenantID, userID).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []SessionSummary{}
	}
	return out, nil
}

// RenameSession sets the display title, scoped to the tenant + user.
func (s *GormStore) RenameSession(ctx context.Context, id, tenantID, userID, title string) error {
	res := s.db.WithContext(ctx).Model(&sessionRow{}).
		Where("id = ? AND tenant_id = ? AND user_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid", id, tenantID, userID).
		Updates(map[string]any{"title": title, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteSession removes a session (messages cascade), scoped to the tenant + user.
func (s *GormStore) DeleteSession(ctx context.Context, id, tenantID, userID string) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND user_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid", id, tenantID, userID).
		Delete(&sessionRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// nullableString returns nil for an empty string (NULL uuid column).
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
