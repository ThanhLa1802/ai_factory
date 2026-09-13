package billing

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// reapBatch bounds how many expired holds one sweep releases.
const reapBatch = 500

// Reaper periodically releases wallet holds whose lease lapsed (a process died
// between reserve and settle). Safe to run on every API replica: the underlying
// ReleaseExpired locks rows with SKIP LOCKED.
type Reaper struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewReaper builds a reaper. log may be nil (defaults to slog.Default).
func NewReaper(svc *Service, log *slog.Logger, interval time.Duration) *Reaper {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	return &Reaper{svc: svc, log: log, interval: interval}
}

// Start begins reaping in the background until ctx is cancelled or Close is
// called. Safe to call once.
func (r *Reaper) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			if n, err := r.svc.ReapExpired(ctx, reapBatch); err != nil {
				r.log.Error("billing reaper", "err", err)
			} else if n > 0 {
				r.log.Info("billing holds released", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Close stops the background loop (DI Closer lifecycle).
func (r *Reaper) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}
