package outbox

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/ai-factory/go-server/internal/infrastructure/message"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// fakeProducer records published events; setting fail makes Publish return an
// error so the failure path can be exercised.
type fakeProducer struct {
	mu        sync.Mutex
	published []message.Event
	fail      bool
}

func (p *fakeProducer) Publish(_ context.Context, _ string, ev message.Event) error {
	if p.fail {
		return errors.New("producer down")
	}
	p.mu.Lock()
	p.published = append(p.published, ev)
	p.mu.Unlock()
	return nil
}

func (p *fakeProducer) Close() error { return nil }

func (p *fakeProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

func openDB(t *testing.T) *database.DB {
	t.Helper()
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := d.Gorm().Exec("DELETE FROM outbox").Error; err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	return d
}

// TestStoreEnqueueInTransaction: an event enqueued inside a transaction is only
// visible after commit, and rollback discards it.
func TestStoreEnqueueInTransaction(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := NewStore(d.Gorm())
	tenant := uuid.NewString()

	committed := message.NewEvent(message.TypeDeploymentCreated, tenant, "d-1", nil)
	if err := d.Gorm().Transaction(func(tx *gorm.DB) error {
		return EnqueueTx(tx, message.TopicDeploymentEvents, committed)
	}); err != nil {
		t.Fatalf("enqueue committed: %v", err)
	}

	rolledBack := message.NewEvent(message.TypeDeploymentCreated, tenant, "d-2", nil)
	if err := d.Gorm().Transaction(func(tx *gorm.DB) error {
		if err := EnqueueTx(tx, message.TopicDeploymentEvents, rolledBack); err != nil {
			return err
		}
		return errors.New("force rollback")
	}); err == nil {
		t.Fatal("expected rollback error")
	}

	rows, err := store.FetchBatch(ctx, 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (rolled-back event must not appear)", len(rows))
	}
	if rows[0].EventID != committed.ID {
		t.Fatalf("event_id = %s, want %s", rows[0].EventID, committed.ID)
	}
}

// TestPublisherDrainDeliversAndMarks: a successful drain publishes the envelope
// (same event_id) and removes the row from the unpublished batch.
func TestPublisherDrainDeliversAndMarks(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := NewStore(d.Gorm())
	tenant := uuid.NewString()

	ev := message.NewEvent(message.TypeDeploymentCreated, tenant, "d-1", map[string]any{"name": "svc"})
	if err := store.Enqueue(ctx, message.TopicDeploymentEvents, ev); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	producer := &fakeProducer{}
	pub := NewPublisher(store, producer, nil)
	n, err := pub.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 1 || producer.count() != 1 {
		t.Fatalf("drained=%d published=%d, want 1/1", n, producer.count())
	}
	if producer.published[0].ID != ev.ID {
		t.Fatalf("published event_id = %s, want %s", producer.published[0].ID, ev.ID)
	}
	rows, err := store.FetchBatch(ctx, 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("unpublished rows = %d, want 0", len(rows))
	}
}

// TestPublisherFailureLeavesRowUnpublished: a failing producer increments
// attempts and keeps the row for the next poll (at-least-once).
func TestPublisherFailureLeavesRowUnpublished(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := NewStore(d.Gorm())
	tenant := uuid.NewString()

	if err := store.Enqueue(ctx, message.TopicDeploymentEvents,
		message.NewEvent(message.TypeDeploymentStopRequested, tenant, "d-1", nil)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	pub := NewPublisher(store, &fakeProducer{fail: true}, nil)
	n, err := pub.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 0 {
		t.Fatalf("drained = %d, want 0", n)
	}
	rows, err := store.FetchBatch(ctx, 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 1 || rows[0].Attempts != 1 || rows[0].LastError == "" {
		t.Fatalf("rows = %+v, want 1 row with attempts=1 and last_error set", rows)
	}
}
