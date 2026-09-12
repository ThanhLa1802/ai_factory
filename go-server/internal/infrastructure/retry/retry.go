// Package retry provides a small exponential-backoff retry helper with full
// jitter and context cancellation. Used by the deployment worker so a
// transient runtime blip does not permanently fail a deployment (roadmap A5 —
// reliability: retry / backoff / jitter).
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Options configures Do.
type Options struct {
	// Attempts is the total number of tries (>= 1). Retries = Attempts - 1.
	Attempts int
	// InitialDelay is the backoff before the second attempt; it doubles with
	// each further retry (plus jitter).
	InitialDelay time.Duration
}

// permanentError marks an error as non-retryable.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as non-retryable: Do returns it immediately without
// further attempts.
func Permanent(err error) error { return &permanentError{err: err} }

// Do runs fn until it returns nil, retries are exhausted, ctx is cancelled, or
// fn returns a retry.Permanent error. Between attempts it sleeps with
// exponential backoff plus full jitter: the delay before attempt n is drawn
// from [base*2^(n-2)/2, 3*base*2^(n-2)/2).
func Do(ctx context.Context, o Options, fn func() error) error {
	if o.Attempts < 1 {
		o.Attempts = 1
	}
	if o.InitialDelay <= 0 {
		o.InitialDelay = 50 * time.Millisecond
	}
	delay := o.InitialDelay
	for i := 1; ; i++ {
		err := fn()
		if err == nil {
			return nil
		}
		var pe *permanentError
		if errors.As(err, &pe) {
			return pe.err
		}
		if i >= o.Attempts {
			return err
		}
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
		delay = o.InitialDelay * time.Duration(1<<uint(i))
		delay = delay/2 + time.Duration(rand.Int64N(int64(delay)))
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
