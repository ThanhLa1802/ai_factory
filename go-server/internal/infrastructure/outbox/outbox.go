// Package outbox implements the transactional outbox pattern (Phase 6): an event
// is written to the `outbox` table in the same transaction as the domain change,
// and a background publisher drains it to the event bus. This removes the window
// where a committed change could lose its event if the process dies before
// publishing.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Record is one row of the outbox. The whole event envelope is stored as JSONB
// so the publisher re-emits it byte-for-byte (same event_id/trace_id).
type Record struct {
	ID          string     `gorm:"primaryKey;type:uuid"`
	Topic       string     `gorm:"column:topic;not null"`
	EventID     string     `gorm:"column:event_id;not null"`
	EventType   string     `gorm:"column:event_type;not null"`
	TenantID    string     `gorm:"column:tenant_id;not null"`
	ResourceID  string     `gorm:"column:resource_id;not null"`
	Event       []byte     `gorm:"column:event;type:jsonb;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null"`
	PublishedAt *time.Time `gorm:"column:published_at"`
	Attempts    int        `gorm:"column:attempts;not null;default:0"`
	LastError   string     `gorm:"column:last_error;not null;default:''"`
}

// TableName pins the table name to the migration.
func (Record) TableName() string { return "outbox" }

// Store reads and writes outbox rows.
type Store struct{ db *gorm.DB }

// NewStore builds a store over a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Enqueue inserts an event on its own connection. Use EnqueueTx when the event
// must commit atomically with another write.
func (s *Store) Enqueue(ctx context.Context, topic string, ev message.Event) error {
	return EnqueueTx(s.db.WithContext(ctx), topic, ev)
}

// EnqueueTx inserts an event using the caller's transaction so it commits (or
// rolls back) together with the domain change.
func EnqueueTx(tx *gorm.DB, topic string, ev message.Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	rec := Record{
		ID:         uuid.NewString(),
		Topic:      topic,
		EventID:    ev.ID,
		EventType:  ev.Type,
		TenantID:   ev.TenantID,
		ResourceID: ev.ResourceID,
		Event:      raw,
		CreatedAt:  time.Now().UTC(),
	}
	if err := tx.Create(&rec).Error; err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

// FetchBatch returns up to limit unpublished rows in write order.
func (s *Store) FetchBatch(ctx context.Context, limit int) ([]Record, error) {
	var rows []Record
	if err := s.db.WithContext(ctx).
		Where("published_at IS NULL").
		Order("created_at").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("fetch outbox: %w", err)
	}
	return rows, nil
}

// MarkPublished stamps a row as delivered.
func (s *Store) MarkPublished(ctx context.Context, id string) error {
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&Record{}).Where("id = ?", id).
		Updates(map[string]any{"published_at": now, "last_error": ""}).Error; err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

// MarkFailed records a delivery failure and leaves the row unpublished for the
// next poll (at-least-once).
func (s *Store) MarkFailed(ctx context.Context, id string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := s.db.WithContext(ctx).Model(&Record{}).Where("id = ?", id).
		Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "last_error": msg}).Error; err != nil {
		return fmt.Errorf("mark outbox failed: %w", err)
	}
	return nil
}
