package events

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// TestKafkaEventBusRoundTrip runs only when Kafka is reachable (AI_FACTORY_KAFKA_ADDR
// or the localhost:9092 default). It uses a unique consumer group so it never
// competes with a running deployment worker.
func TestKafkaEventBusRoundTrip(t *testing.T) {
	addr := os.Getenv("AI_FACTORY_KAFKA_ADDR")
	if addr == "" {
		addr = "localhost:9092"
	}
	if conn, err := kafka.Dial("tcp", addr); err != nil {
		t.Skipf("kafka not reachable at %s: %v", addr, err)
	} else {
		_ = conn.Close()
	}

	bus, err := NewKafkaEventBusWithGroup(addr, "test-"+time.Now().Format("150405"))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	want := NewEvent(TypeDeploymentCreated, "tenant-t", "deploy-t", nil)

	received := make(chan Event, 1)
	if err := bus.Subscribe(ctx, TopicDeploymentEvents,
		func(ctx context.Context, ev Event) error {
			if ev.ID != want.ID {
				return nil // skip stale events replayed from the topic head (new group starts at first offset)
			}
			received <- ev
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(ctx, TopicDeploymentEvents, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-received:
		if got.ID != want.ID || got.Type != want.Type {
			t.Fatalf("received = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for event round-trip: %v", ctx.Err())
	}
}
