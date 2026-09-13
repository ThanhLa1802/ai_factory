package outbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/message"
)

const (
	defaultInterval = 500 * time.Millisecond
	defaultBatch    = 100
)

// Publisher drains unpublished outbox rows to the event bus. Delivery is
// at-least-once: a row is stamped published_at only after Publish returns nil;
// failures bump attempts and leave the row for the next poll. Consumers are
// expected to be idempotent (the deployment worker already is).
type Publisher struct {
	store    *Store
	producer message.Producer
	log      *slog.Logger
	interval time.Duration
	batch    int

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPublisher builds a publisher with the default poll interval/batch size.
func NewPublisher(store *Store, producer message.Producer, log *slog.Logger) *Publisher {
	if log == nil {
		log = slog.Default()
	}
	return &Publisher{store: store, producer: producer, log: log, interval: defaultInterval, batch: defaultBatch}
}

// Start begins draining in the background until ctx is cancelled or Close is
// called. It is safe to call once.
func (p *Publisher) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			if n, err := p.DrainOnce(ctx); err != nil {
				p.log.Error("outbox drain", "err", err)
			} else if n > 0 {
				p.log.Info("outbox published", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// DrainOnce publishes one batch synchronously and returns the number delivered.
// Exported so tests can flush deterministically.
func (p *Publisher) DrainOnce(ctx context.Context) (int, error) {
	rows, err := p.store.FetchBatch(ctx, p.batch)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, rec := range rows {
		var ev message.Event
		if err := json.Unmarshal(rec.Event, &ev); err != nil {
			if mErr := p.store.MarkFailed(ctx, rec.ID, err); mErr != nil {
				return published, mErr
			}
			continue
		}
		if err := p.producer.Publish(ctx, rec.Topic, ev); err != nil {
			if mErr := p.store.MarkFailed(ctx, rec.ID, err); mErr != nil {
				return published, mErr
			}
			continue
		}
		if err := p.store.MarkPublished(ctx, rec.ID); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

// Close stops the background loop (DI Closer lifecycle). The event bus itself is
// owned by the composition root and is not closed here.
func (p *Publisher) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	return nil
}
