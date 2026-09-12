// Package circuitbreaker provides a small, thread-safe 3-state circuit
// breaker (closed → open → half-open). Used by the deployment worker so a
// known-down runtime is not hammered with retries: while the breaker is open,
// calls fail fast with ErrOpen instead of invoking the underlying operation
// (roadmap A5 — reliability: circuit breaker).
package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State of a circuit breaker.
type State int

const (
	// StateClosed allows calls normally.
	StateClosed State = iota
	// StateOpen rejects calls with ErrOpen until the cooldown elapses.
	StateOpen
	// StateHalfOpen allows a single probe call after the cooldown.
	StateHalfOpen
)

// ErrOpen is returned by Call when the breaker is open and the call is
// rejected without invoking the wrapped function.
var ErrOpen = errors.New("circuit breaker open")

// CircuitBreaker trips open after `threshold` consecutive failures and allows
// one probe after `cooldown`. A successful probe (or any success in closed
// state) resets the failure count.
type CircuitBreaker struct {
	mu        sync.Mutex
	failures  int
	state     State
	openedAt  time.Time
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

// New returns a breaker that opens after `threshold` consecutive failures and
// allows one probe after `cooldown`.
func New(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		state:     StateClosed,
		now:       time.Now,
	}
}

// State returns the current breaker state.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == StateOpen && cb.now().Sub(cb.openedAt) >= cb.cooldown {
		cb.state = StateHalfOpen
	}
	return cb.state
}

// Call runs fn when the breaker permits. While open it returns ErrOpen without
// invoking fn. On success the failure count resets to closed; on failure the
// count increments and the breaker may trip open.
func (cb *CircuitBreaker) Call(ctx context.Context, fn func() error) error {
	cb.mu.Lock()
	if cb.state == StateOpen {
		if cb.now().Sub(cb.openedAt) < cb.cooldown {
			cb.mu.Unlock()
			return ErrOpen
		}
		cb.state = StateHalfOpen
	}
	halfOpen := cb.state == StateHalfOpen
	cb.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()
	if err == nil {
		cb.failures = 0
		cb.state = StateClosed
		return nil
	}
	cb.failures++
	if halfOpen || cb.failures >= cb.threshold {
		cb.state = StateOpen
		cb.openedAt = cb.now()
	}
	return err
}
