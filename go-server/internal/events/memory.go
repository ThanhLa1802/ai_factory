package events

import (
	"context"
	"fmt"
	"sync"
)

// MemoryEventBus is an in-process Producer+Consumer used by tests and as a
// graceful fallback when Kafka is unreachable. Dispatch is synchronous, which
// makes worker orchestration deterministic in tests.
type MemoryEventBus struct {
	mu       sync.Mutex
	handlers map[string][]func(ctx context.Context, ev Event) error
}

func NewMemoryEventBus() *MemoryEventBus {
	return &MemoryEventBus{handlers: map[string][]func(ctx context.Context, ev Event) error{}}
}

func (b *MemoryEventBus) Publish(ctx context.Context, topic string, ev Event) error {
	b.mu.Lock()
	handlers := append([]func(context.Context, Event) error(nil), b.handlers[topic]...)
	b.mu.Unlock()
	for _, h := range handlers {
		if err := h(ctx, ev); err != nil {
			return fmt.Errorf("memory event handler for %s: %w", ev.Type, err)
		}
	}
	return nil
}

func (b *MemoryEventBus) Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, ev Event) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
	return nil
}

func (b *MemoryEventBus) Close() error { return nil }
