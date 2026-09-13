package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	flushInterval = 30 * time.Second
	// flushLockKey must not start with the counter prefix ("usage:") or the
	// drain scan would pick up the lock key itself (WRONGTYPE).
	flushLockKey = "lock:usage:flush"
	// flushLockTTL is shorter than the interval so a crashed flusher's lock
	// self-releases before the next cycle.
	flushLockTTL = 25 * time.Second
)

// Flusher periodically drains the Redis usage counter into the aggregate table.
// A distributed lock ensures only one replica flushes per cycle; the drain is
// read-and-delete, so buckets are never counted twice.
type Flusher struct {
	counter  Counter
	aggs     AggregateRepository
	lock     Locker
	log      *slog.Logger
	interval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewFlusher builds a flusher. log may be nil (defaults to slog.Default).
func NewFlusher(counter Counter, aggs AggregateRepository, lock Locker, log *slog.Logger) *Flusher {
	if log == nil {
		log = slog.Default()
	}
	return &Flusher{counter: counter, aggs: aggs, lock: lock, log: log, interval: flushInterval}
}

// Start begins flushing in the background until ctx is cancelled or Close is
// called. Safe to call once.
func (f *Flusher) Start(ctx context.Context) {
	ctx, f.cancel = context.WithCancel(ctx)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		ticker := time.NewTicker(f.interval)
		defer ticker.Stop()
		for {
			if n, err := f.FlushOnce(ctx); err != nil {
				f.log.Error("usage flush", "err", err)
			} else if n > 0 {
				f.log.Info("usage flushed", "buckets", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// FlushOnce drains and upserts one batch. It is a no-op (0, nil) when another
// replica holds the flush lock. Exported so tests can flush deterministically.
func (f *Flusher) FlushOnce(ctx context.Context) (int, error) {
	token, ok, err := f.lock.Acquire(ctx, flushLockKey, flushLockTTL)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil // another replica is flushing
	}
	defer func() {
		// Release even if ctx was cancelled mid-flush (shutdown).
		_ = f.lock.Release(context.WithoutCancel(ctx), flushLockKey, token)
	}()

	buckets, err := f.counter.Drain(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range buckets {
		if err := f.aggs.Upsert(ctx, b); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Close stops the background loop (DI Closer lifecycle).
func (f *Flusher) Close() error {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	return nil
}
