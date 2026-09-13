package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// rollInterval is how often the roller folds new usage_events into usage_daily.
// Reads are served from the rollup, so this bounds the visible lag.
const rollInterval = 5 * time.Second

// Roller periodically folds usage_events into the usage_daily rollup. The fold
// is transactional and watermark-driven (see AggregateRepository.Rollup), so it
// is idempotent and safe to run on every API replica: concurrent rollers
// serialise on the watermark row and re-processing a batch never double counts.
type Roller struct {
	aggs     AggregateRepository
	log      *slog.Logger
	interval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRoller builds a roller. log may be nil (defaults to slog.Default).
func NewRoller(aggs AggregateRepository, log *slog.Logger) *Roller {
	if log == nil {
		log = slog.Default()
	}
	return &Roller{aggs: aggs, log: log, interval: rollInterval}
}

// Start begins rolling in the background until ctx is cancelled or Close is
// called. Safe to call once.
func (r *Roller) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			if n, err := r.RollOnce(ctx); err != nil {
				r.log.Error("usage rollup", "err", err)
			} else if n > 0 {
				r.log.Info("usage rolled up", "buckets", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// RollOnce drains one batch of pending events into the rollup. Exported so
// tests can roll deterministically.
func (r *Roller) RollOnce(ctx context.Context) (int, error) { return r.aggs.Rollup(ctx) }

// Close stops the background loop (DI Closer lifecycle).
func (r *Roller) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}
