package events

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryBusDelivers(t *testing.T) {
	bus := NewMemoryEventBus()
	var got Event
	if err := bus.Subscribe(context.Background(), TopicDeploymentEvents,
		func(ctx context.Context, ev Event) error { got = ev; return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	want := NewEvent(TypeDeploymentCreated, "t1", "d1", nil)
	if err := bus.Publish(context.Background(), TopicDeploymentEvents, want); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got.ID != want.ID || got.Type != want.Type {
		t.Fatalf("delivered = %+v, want %+v", got, want)
	}
}

func TestMemoryBusMultipleSubscribers(t *testing.T) {
	bus := NewMemoryEventBus()
	calls := 0
	for i := 0; i < 3; i++ {
		if err := bus.Subscribe(context.Background(), TopicDeploymentEvents,
			func(ctx context.Context, ev Event) error { calls++; return nil }); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	if err := bus.Publish(context.Background(), TopicDeploymentEvents, NewEvent(TypeDeploymentReady, "t", "d", nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if calls != 3 {
		t.Fatalf("handler calls = %d, want 3", calls)
	}
}

func TestMemoryBusHandlerErrorPropagates(t *testing.T) {
	bus := NewMemoryEventBus()
	if err := bus.Subscribe(context.Background(), TopicDeploymentEvents,
		func(ctx context.Context, ev Event) error { return errors.New("boom") }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), TopicDeploymentEvents, NewEvent(TypeDeploymentCreated, "t", "d", nil)); err == nil {
		t.Fatal("publish error = nil, want handler error to propagate")
	}
}
