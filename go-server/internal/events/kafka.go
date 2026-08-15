package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/segmentio/kafka-go"
)

// KafkaEventBus implements Producer and Consumer on Apache Kafka.
type KafkaEventBus struct {
	addr   string
	writer *kafka.Writer
	group  string
	mu     sync.Mutex
	reader *kafka.Reader
	log    *slog.Logger
}

// NewKafkaEventBus connects to Kafka at addr (fail fast: it Dials now so the
// caller can decide whether Kafka is mandatory or optional at boot) and
// returns a bus whose consumer group is "deployment-worker".
func NewKafkaEventBus(addr string) (*KafkaEventBus, error) {
	return NewKafkaEventBusWithGroup(addr, "deployment-worker")
}

// NewKafkaEventBusWithGroup is NewKafkaEventBus with an explicit consumer
// group id (used by tests to avoid stealing the real worker's group).
func NewKafkaEventBusWithGroup(addr, groupID string) (*KafkaEventBus, error) {
	conn, err := kafka.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("kafka unreachable at %s: %w", addr, err)
	}
	// Ensure the deployment topic exists before the first publish. Kafka's
	// auto-create is asynchronous (and kafka-go's writer does not trigger it on
	// the very first write), so relying on it makes the first deployment event
	// fail on a fresh broker. CreateTopics is idempotent (already-exists is
	// ignored), and fails fast here so the caller can surface the error at boot.
	if err := conn.CreateTopics(kafka.TopicConfig{Topic: TopicDeploymentEvents, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("kafka: ensure topic %s exists: %w", TopicDeploymentEvents, err)
	}
	_ = conn.Close()
	return &KafkaEventBus{
		addr: addr,
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(addr),
			RequiredAcks:           kafka.RequireAll,
			AllowAutoTopicCreation: true,
		},
		group: groupID,
		log:   slog.Default(),
	}, nil
}

func (b *KafkaEventBus) Publish(ctx context.Context, topic string, ev Event) error {
	val, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	// Partition by resource_id so all events for one deployment stay ordered.
	if err := b.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(ev.ResourceID),
		Value: val,
	}); err != nil {
		return fmt.Errorf("publish %s: %w", ev.Type, err)
	}
	return nil
}

// Subscribe registers the handler and returns immediately; consumption runs on
// its own goroutine.
func (b *KafkaEventBus) Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, ev Event) error) error {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{b.addr},
		GroupID:  b.group,
		Topic:    topic,
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	b.mu.Lock()
	b.reader = r
	b.mu.Unlock()
	go b.consume(ctx, r, handler)
	return nil
}

// consume drives the read loop. At-least-once: a message is committed only
// after its handler returns nil; on a persistent handler error the consumer
// stops so the uncommitted message is redelivered on restart (the worker's
// state-machine guard makes replays idempotent).
func (b *KafkaEventBus) consume(ctx context.Context, r *kafka.Reader, handler func(ctx context.Context, ev Event) error) {
	for {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		var ev Event
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			_ = r.CommitMessages(ctx, m) // non-envelope noise: skip it
			continue
		}
		if err := handler(ctx, ev); err != nil {
			b.log.Error("event handler failed; consumer stopping for redelivery", "event_id", ev.ID, "err", err)
			return
		}
		_ = r.CommitMessages(ctx, m)
	}
}

func (b *KafkaEventBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.writer.Close(); err != nil {
		return err
	}
	if b.reader != nil {
		return b.reader.Close()
	}
	return nil
}
