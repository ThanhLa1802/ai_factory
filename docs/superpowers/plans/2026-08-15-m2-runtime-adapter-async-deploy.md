# M2: Runtime Adapter + Async Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ServingRuntimeAdapter` + `ComputeProvider` (spec §3.3/§3.4), a Kafka-backed event bus, and a deployment worker so `POST /api/v1/deployments` returns 202 and the deployment drives itself `PENDING → PROVISIONING → STARTING → READY` asynchronously, emitting events along the way.

**Architecture:** Two new packages. `internal/events` defines the spec §6 event envelope + topics and a `Producer`/`Consumer` interface with two implementations: `KafkaEventBus` (segmentio/kafka-go) and `MemoryEventBus` (in-process, used by tests and as a graceful fallback when Kafka is down). `internal/runtime` defines `ServingRuntimeAdapter` (§3.3), `ComputeProvider` (§3.4), a `WorkerAdapter` wrapping the Python worker gRPC address (deployment lifecycle is spec-validated only — the worker has no deployment RPC yet, documented M2 boundary), `MockComputeProvider` (dev), and the deployment `Worker` (Kafka consumer goroutine that drives the state machine and emits `deployment_ready`/`deployment_failed`/`deployment_stopped`). The API handlers publish `deployment_created` / `deployment_stop_requested` instead of transitioning state directly.

**Tech Stack:** Go 1.25.7, `github.com/segmentio/kafka-go` (new dep, pure Go), pgx, goose, existing `controlplane`/`auth`/`api` packages.

## Global Constraints

All constraints from the M1 plan still hold and bind every task:

- Module `github.com/ai-factory/go-server`, Go 1.25.7 (see `go.mod`).
- Entity IDs are UUIDs (`github.com/google/uuid`); all timestamps UTC.
- JSON API errors are `{"error":{"code":"...","message":"..."}}` (`api.writeAPIError`).
- Tenant is derived from JWT claims (`auth.ClaimsFromContext`), never trusted from the request body.
- Migrations are goose embed files at `go-server/internal/db/migrations/`, named `NNNN_snake_case.sql`; migrations table is managed by goose (already wired in `db.Connect`/`Migrate`).
- Docker build context = repo root (unchanged).
- **Security (standing project rule):** never log API keys, passwords, or raw prompts. Deployment error strings may be logged/published; secrets may not.

M2-specific constraints:

- **Event envelope (spec §6.2), exact JSON field names:** `{"event_id","event_type","event_version","timestamp","tenant_id","resource_id","trace_id","payload"}`. `event_version` is `1`.
- **Event topics (spec §6.1), exact strings:** `serving.deployment.events`, `serving.inference.events`, `serving.audit.events`. M2 uses only the deployment topic.
- **Kafka client:** `github.com/segmentio/kafka-go` (pure Go, no cgo).
- **Kafka address:** env `AI_FACTORY_KAFKA_ADDR`, default `localhost:9092`.
- **Kafka is OPTIONAL at boot** — deliberate deviation from the Postgres hard requirement, documented in the plan: the existing chat/inference path must boot and serve without Kafka. If Kafka is unreachable, the server logs a WARN, falls back to `MemoryEventBus` (deployments stay `PENDING`, control plane + chat still work), and skips the deployment worker. Only when Kafka is reachable does the worker start.
- **Consumption is at-least-once, idempotent by state machine:** the consumer stops on a persistent handler error so the uncommitted message is redelivered on restart; the worker's state guard (`deployment_created` on a deployment already `READY`/in-flight/`FAILED` is a no-op) makes replays safe.
- **State machine (unchanged, `controlplane/state.go`):** `PENDING → PROVISIONING → STARTING → READY`, every non-terminal state → `FAILED` (terminal), `READY/DEGRADED → STOPPING → STOPPED`, `STOPPED → PENDING`.
- Every task is TDD: write the failing test, watch it fail, implement, watch it pass, `go vet ./...`, commit.

## File Structure

| File | Responsibility |
|---|---|
| `go-server/internal/events/events.go` | `Event` envelope (§6.2), `NewEvent`, topic + type constants (§6.1), `Producer`/`Consumer` interfaces |
| `go-server/internal/events/events_test.go` | envelope JSON field-name test |
| `go-server/internal/events/memory.go` | `MemoryEventBus` (in-process Producer+Consumer, tests + fallback) |
| `go-server/internal/events/memory_test.go` | bus delivery / multi-subscriber / error propagation tests |
| `go-server/internal/events/kafka.go` | `KafkaEventBus` (segmentio/kafka-go Producer+Consumer, partition key = resource_id) |
| `go-server/internal/events/kafka_test.go` | gated live-Kafka round-trip test |
| `go-server/internal/config/config.go` | add `KafkaAddr` (`AI_FACTORY_KAFKA_ADDR`, default `localhost:9092`) |
| `go-server/internal/config/config_test.go` | KafkaAddr default + env-override assertions |
| `go-server/internal/runtime/runtime.go` | `ServingRuntimeAdapter` (§3.3), `ComputeProvider` (§3.4), `DeploymentStore` interfaces |
| `go-server/internal/runtime/worker_adapter.go` | `WorkerAdapter` — spec-validating lifecycle + TCP `HealthCheck` |
| `go-server/internal/runtime/mock.go` | `MockComputeProvider` (dev) |
| `go-server/internal/runtime/runtime_test.go` | adapter validate/healthcheck + mock provider tests |
| `go-server/internal/runtime/worker.go` | `Worker` — orchestrates `deployment_created`/`deployment_stop_requested` → state transitions + events |
| `go-server/internal/runtime/worker_test.go` | worker orchestration with an in-memory fake store |
| `go-server/internal/db/migrations/0002_deployment_workload_ref.sql` | add `workload_ref` column to `deployments` |
| `go-server/internal/controlplane/deployment.go` | `Deployment.WorkloadRef`, `Endpoint`, `SetWorkloadRef`, `CreateEndpoint`; SELECTs include `workload_ref` |
| `go-server/internal/controlplane/deployment_test.go` | gated integration: workload_ref round-trip + endpoint |
| `go-server/internal/api/controlplane.go` | handler takes `events.Producer`; create publishes `deployment_created`; action publishes events |
| `go-server/internal/api/controlplane_test.go` | update `TestLoginE2E` constructor call; new `TestAsyncDeployE2E` |
| `go-server/cmd/server/main.go` | construct bus (warn+fallback), start worker, pass bus to handler |
| `deployments/docker-compose.yml` | server env `AI_FACTORY_KAFKA_ADDR: kafka:9092` (commented note re container networking) |
| `scripts/m2-demo.sh` | demo: login → model/template/version → deploy → poll to READY |
| `CLAUDE.md`, `docs/TRACKING.md` | update architecture + roadmap (M2 done) |

---

### Task 1: Event envelope + Kafka/Memory event bus + config KafkaAddr

**Files:**
- Create: `go-server/internal/events/events.go`
- Create: `go-server/internal/events/events_test.go`
- Create: `go-server/internal/events/memory.go`
- Create: `go-server/internal/events/memory_test.go`
- Create: `go-server/internal/events/kafka.go`
- Create: `go-server/internal/events/kafka_test.go`
- Modify: `go-server/internal/config/config.go`
- Modify: `go-server/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing new (uses `github.com/google/uuid`, `github.com/segmentio/kafka-go`).
- Produces (used by Task 4 + Task 5):
  - `type Event struct` with fields `ID string` (`json:"event_id"`), `Type string` (`json:"event_type"`), `Version int` (`json:"event_version"`), `Timestamp time.Time` (`json:"timestamp"`), `TenantID string` (`json:"tenant_id"`), `ResourceID string` (`json:"resource_id"`), `TraceID string` (`json:"trace_id"`), `Payload map[string]any` (`json:"payload"`).
  - `func NewEvent(eventType, tenantID, resourceID string, payload map[string]any) Event`
  - `const TopicDeploymentEvents = "serving.deployment.events"`, `TopicInferenceEvents`, `TopicAuditEvents`
  - `const TypeDeploymentCreated = "deployment_created"`, `TypeDeploymentReady = "deployment_ready"`, `TypeDeploymentFailed = "deployment_failed"`, `TypeDeploymentStopRequested = "deployment_stop_requested"`, `TypeDeploymentStopped = "deployment_stopped"`
  - `type Producer interface { Publish(ctx context.Context, topic string, ev Event) error; Close() error }`
  - `type Consumer interface { Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, ev Event) error) error; Close() error }`
  - `func NewKafkaEventBus(addr string) (*KafkaEventBus, error)` — group `"deployment-worker"`
  - `func NewKafkaEventBusWithGroup(addr, groupID string) (*KafkaEventBus, error)` — group given (test isolation)
  - `func NewMemoryEventBus() *MemoryEventBus`
  - Both `*KafkaEventBus` and `*MemoryEventBus` implement `Producer` + `Consumer`.

- [ ] **Step 1: Write the failing envelope test**

Create `go-server/internal/events/events_test.go`:

```go
package events

import (
	"encoding/json"
	"testing"
)

// TestEventEnvelopeJSON asserts the wire format matches spec §6.2 field-for-field.
func TestEventEnvelopeJSON(t *testing.T) {
	ev := NewEvent(TypeDeploymentCreated, "tenant-1", "deploy-1", map[string]any{"name": "svc"})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"event_id", "event_type", "event_version", "timestamp", "tenant_id", "resource_id", "trace_id", "payload"} {
		if _, ok := got[k]; !ok {
			t.Errorf("envelope missing key %q (raw: %s)", k, raw)
		}
	}
	if got["event_type"] != TypeDeploymentCreated {
		t.Errorf("event_type = %v, want %s", got["event_type"], TypeDeploymentCreated)
	}
	if got["tenant_id"] != "tenant-1" || got["resource_id"] != "deploy-1" {
		t.Errorf("tenant/resource mismatch: %s", raw)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd go-server && go test ./internal/events/ -run TestEventEnvelopeJSON -v`
Expected: FAIL — `no Go files in .../internal/events` (package does not exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `go-server/internal/events/events.go`:

```go
package events

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Topics per spec §6.1. M2 publishes/consumes only the deployment topic.
const (
	TopicDeploymentEvents = "serving.deployment.events"
	TopicInferenceEvents  = "serving.inference.events"
	TopicAuditEvents      = "serving.audit.events"
)

// Event types on the deployment topic.
const (
	TypeDeploymentCreated       = "deployment_created"
	TypeDeploymentReady         = "deployment_ready"
	TypeDeploymentFailed        = "deployment_failed"
	TypeDeploymentStopRequested = "deployment_stop_requested"
	TypeDeploymentStopped       = "deployment_stopped"
)

// Event is the spec §6.2 envelope. JSON field names are part of the contract —
// do not rename without updating the spec and every consumer.
type Event struct {
	ID         string         `json:"event_id"`
	Type       string         `json:"event_type"`
	Version    int            `json:"event_version"`
	Timestamp  time.Time      `json:"timestamp"`
	TenantID   string         `json:"tenant_id"`
	ResourceID string         `json:"resource_id"`
	TraceID    string         `json:"trace_id"`
	Payload    map[string]any `json:"payload"`
}

// NewEvent builds an envelope with fresh UUIDs and a UTC timestamp.
func NewEvent(eventType, tenantID, resourceID string, payload map[string]any) Event {
	return Event{
		ID:         uuid.NewString(),
		Type:       eventType,
		Version:    1,
		Timestamp:  time.Now().UTC(),
		TenantID:   tenantID,
		ResourceID: resourceID,
		TraceID:    uuid.NewString(),
		Payload:    payload,
	}
}

// Producer publishes events to a topic.
type Producer interface {
	Publish(ctx context.Context, topic string, ev Event) error
	Close() error
}

// Consumer subscribes a handler to a topic. Subscribe must return promptly
// (handlers run on their own goroutines); Close releases resources.
type Consumer interface {
	Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, ev Event) error) error
	Close() error
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd go-server && go test ./internal/events/ -run TestEventEnvelopeJSON -v`
Expected: PASS.

- [ ] **Step 5: Write the failing memory-bus tests**

Create `go-server/internal/events/memory_test.go`:

```go
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
```

- [ ] **Step 6: Run to verify they fail**

Run: `cd go-server && go test ./internal/events/ -run 'TestMemoryBus' -v`
Expected: FAIL — `undefined: NewMemoryEventBus`.

- [ ] **Step 7: Implement the memory bus**

Create `go-server/internal/events/memory.go`:

```go
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
```

- [ ] **Step 8: Run to verify they pass**

Run: `cd go-server && go test ./internal/events/ -run 'TestMemoryBus' -v`
Expected: PASS (3 tests).

- [ ] **Step 9: Add the Kafka dependency and implement KafkaEventBus**

Run: `cd go-server && go get github.com/segmentio/kafka-go`

Create `go-server/internal/events/kafka.go`:

```go
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
	_ = conn.Close()
	return &KafkaEventBus{
		addr: addr,
		writer: &kafka.Writer{
			Addr:         kafka.TCP(addr),
			RequiredAcks: kafka.RequireAll,
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
```

- [ ] **Step 10: Write the gated Kafka round-trip test**

Create `go-server/internal/events/kafka_test.go`:

```go
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

	received := make(chan Event, 1)
	if err := bus.Subscribe(ctx, TopicDeploymentEvents,
		func(ctx context.Context, ev Event) error { received <- ev; return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := NewEvent(TypeDeploymentCreated, "tenant-t", "deploy-t", nil)
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
```

- [ ] **Step 11: Run all events tests**

Run: `cd go-server && go test ./internal/events/ -v`
Expected: envelope + 3 memory tests PASS; kafka test either PASS (Kafka up) or SKIP with the "not reachable" reason.

- [ ] **Step 12: Add KafkaAddr to config + tests**

Modify `go-server/internal/config/config.go`:

```go
type Config struct {
	DatabaseURL string // AI_FACTORY_DATABASE_URL (default: local dev compose)
	JWTSecret   string // AI_FACTORY_JWT_SECRET (default "dev-secret-change-me")
	LogLevel    string // AI_FACTORY_LOG_LEVEL (default "info")
	KafkaAddr   string // AI_FACTORY_KAFKA_ADDR (default "localhost:9092")
}
```

and in the `Load()` return literal add:

```go
		KafkaAddr:   env("AI_FACTORY_KAFKA_ADDR", "localhost:9092"),
```

Add to `go-server/internal/config/config_test.go`:

In `TestLoadDefaults`, next to the other `t.Setenv` calls (so the test never depends on the machine's env):

```go
	t.Setenv("AI_FACTORY_KAFKA_ADDR", "")
```

and, after the `LogLevel` assertion:

```go
	if cfg.KafkaAddr != "localhost:9092" {
		t.Errorf("KafkaAddr = %q, want localhost:9092", cfg.KafkaAddr)
	}
```

In `TestLoadFromEnv`, add alongside the other `t.Setenv` calls:

```go
	t.Setenv("AI_FACTORY_KAFKA_ADDR", "localhost:19092")
```

and, after the `LogLevel` assertion:

```go
	if cfg.KafkaAddr != "localhost:19092" {
		t.Errorf("KafkaAddr = %q, want localhost:19092", cfg.KafkaAddr)
	}
```

- [ ] **Step 13: Run config tests**

Run: `cd go-server && go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 14: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: no output (clean).

```bash
git add go-server/internal/events go-server/internal/config
git commit -m "feat(events): event envelope + Kafka/Memory bus + config KafkaAddr"
```

---

### Task 2: Runtime interfaces + WorkerAdapter + MockComputeProvider

**Files:**
- Create: `go-server/internal/runtime/runtime.go`
- Create: `go-server/internal/runtime/worker_adapter.go`
- Create: `go-server/internal/runtime/mock.go`
- Create: `go-server/internal/runtime/runtime_test.go`

**Interfaces:**
- Consumes: `controlplane.Deployment` (`github.com/ai-factory/go-server/internal/controlplane`).
- Produces (used by Task 4 + Task 5):
  - `type ServingRuntimeAdapter interface { Create(ctx, *controlplane.Deployment) error; Start(ctx, *controlplane.Deployment) error; Stop(ctx, *controlplane.Deployment) error; Restart(ctx, *controlplane.Deployment) error; Delete(ctx, *controlplane.Deployment) error; GetStatus(ctx, *controlplane.Deployment) (string, error); HealthCheck(ctx, *controlplane.Deployment) error }`
  - `type ComputeProvider interface { RequestCapacity(ctx, *controlplane.Deployment) (string, error); ReleaseCapacity(ctx, *controlplane.Deployment) error; GetWorkloadStatus(ctx, *controlplane.Deployment) (string, error); UpdateWorkload(ctx, *controlplane.Deployment) error }`
  - `func NewWorkerAdapter(workerAddr string) *WorkerAdapter`
  - `func NewMockComputeProvider() *MockComputeProvider`

- [ ] **Step 1: Write the failing tests**

Create `go-server/internal/runtime/runtime_test.go`:

```go
package runtime

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/ai-factory/go-server/internal/controlplane"
)

func validDeployment() *controlplane.Deployment {
	return &controlplane.Deployment{
		ID: "d1", TenantID: "t1", Name: "svc",
		ModelVersionID: "mv1", TemplateVersionID: "tv1",
		DesiredReplicas: 1, Status: controlplane.DeploymentPending,
	}
}

func TestWorkerAdapterValidatesSpec(t *testing.T) {
	a := NewWorkerAdapter("localhost:1")
	if err := a.Start(context.Background(), validDeployment()); err != nil {
		t.Fatalf("Start(valid) = %v, want nil", err)
	}
	bad := validDeployment()
	bad.DesiredReplicas = 0
	if err := a.Start(context.Background(), bad); err == nil {
		t.Fatal("Start(desired_replicas=0) = nil, want validation error")
	}
	bad = validDeployment()
	bad.ModelVersionID = ""
	if err := a.Stop(context.Background(), bad); err == nil {
		t.Fatal("Stop(empty model_version_id) = nil, want validation error")
	}
}

func TestWorkerAdapterHealthCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	a := NewWorkerAdapter(addr)
	if err := a.HealthCheck(context.Background(), validDeployment()); err != nil {
		t.Fatalf("HealthCheck(open) = %v, want nil", err)
	}

	dead := NewWorkerAdapter("127.0.0.1:1")
	if err := dead.HealthCheck(context.Background(), validDeployment()); err == nil {
		t.Fatal("HealthCheck(closed port) = nil, want error")
	}
}

func TestMockComputeProviderLifecycle(t *testing.T) {
	m := NewMockComputeProvider()
	ctx := context.Background()
	d := validDeployment()

	ref, err := m.RequestCapacity(ctx, d)
	if err != nil {
		t.Fatalf("RequestCapacity: %v", err)
	}
	if !strings.HasPrefix(ref, "mock-wl-") {
		t.Fatalf("workload ref = %q, want mock-wl- prefix", ref)
	}
	if st, err := m.GetWorkloadStatus(ctx, d); err != nil || st != "RUNNING" {
		t.Fatalf("GetWorkloadStatus = %q, %v", st, err)
	}
	if err := m.ReleaseCapacity(ctx, d); err != nil {
		t.Fatalf("ReleaseCapacity: %v", err)
	}
	if _, err := m.GetWorkloadStatus(ctx, d); err == nil {
		t.Fatal("GetWorkloadStatus after release = nil, want error")
	}
	if err := m.UpdateWorkload(ctx, d); err != nil {
		t.Fatalf("UpdateWorkload: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd go-server && go test ./internal/runtime/ -run 'TestWorkerAdapter|TestMock' -v`
Expected: FAIL — `undefined: NewWorkerAdapter` / `undefined: NewMockComputeProvider`.

- [ ] **Step 3: Write the interfaces**

Create `go-server/internal/runtime/runtime.go`:

```go
// Package runtime abstracts how deployments get their compute and serving
// lifecycle (spec §3.3 ServingRuntimeAdapter, §3.4 ComputeProvider).
package runtime

import (
	"context"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// ServingRuntimeAdapter drives a deployment's lifecycle on the serving
// runtime. The Python worker has no deployment RPC yet (M2 boundary) — the
// concrete WorkerAdapter validates the spec and reports the control-plane
// status; real provisioning lands with a future worker API.
type ServingRuntimeAdapter interface {
	Create(ctx context.Context, d *controlplane.Deployment) error
	Start(ctx context.Context, d *controlplane.Deployment) error
	Stop(ctx context.Context, d *controlplane.Deployment) error
	Restart(ctx context.Context, d *controlplane.Deployment) error
	Delete(ctx context.Context, d *controlplane.Deployment) error
	GetStatus(ctx context.Context, d *controlplane.Deployment) (string, error)
	HealthCheck(ctx context.Context, d *controlplane.Deployment) error
}

// ComputeProvider acquires and releases the compute backing a deployment.
// RequestCapacity returns an opaque workload reference bound to the deployment.
type ComputeProvider interface {
	RequestCapacity(ctx context.Context, d *controlplane.Deployment) (string, error)
	ReleaseCapacity(ctx context.Context, d *controlplane.Deployment) error
	GetWorkloadStatus(ctx context.Context, d *controlplane.Deployment) (string, error)
	UpdateWorkload(ctx context.Context, d *controlplane.Deployment) error
}
```

- [ ] **Step 4: Implement WorkerAdapter**

Create `go-server/internal/runtime/worker_adapter.go`:

```go
package runtime

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// WorkerAdapter wraps the Python inference worker. It has no deployment
// lifecycle RPC (proto only exposes Generate/BatchGenerate), so lifecycle
// operations validate the deployment spec and return; HealthCheck probes the
// worker's gRPC port with a TCP dial.
type WorkerAdapter struct {
	workerAddr string
}

func NewWorkerAdapter(workerAddr string) *WorkerAdapter {
	return &WorkerAdapter{workerAddr: workerAddr}
}

func (a *WorkerAdapter) Create(ctx context.Context, d *controlplane.Deployment) error { return a.validate(d) }
func (a *WorkerAdapter) Start(ctx context.Context, d *controlplane.Deployment) error  { return a.validate(d) }
func (a *WorkerAdapter) Stop(ctx context.Context, d *controlplane.Deployment) error   { return a.validate(d) }
func (a *WorkerAdapter) Restart(ctx context.Context, d *controlplane.Deployment) error {
	return a.validate(d)
}
func (a *WorkerAdapter) Delete(ctx context.Context, d *controlplane.Deployment) error { return nil }

// GetStatus reports the control-plane status back; there is no worker-side
// status to query yet.
func (a *WorkerAdapter) GetStatus(ctx context.Context, d *controlplane.Deployment) (string, error) {
	return d.Status, nil
}

// HealthCheck verifies the worker's gRPC endpoint accepts TCP connections.
func (a *WorkerAdapter) HealthCheck(ctx context.Context, d *controlplane.Deployment) error {
	conn, err := net.DialTimeout("tcp", a.workerAddr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("worker unreachable at %s: %w", a.workerAddr, err)
	}
	_ = conn.Close()
	return nil
}

func (a *WorkerAdapter) validate(d *controlplane.Deployment) error {
	if d.ModelVersionID == "" || d.TemplateVersionID == "" || d.DesiredReplicas < 1 {
		return fmt.Errorf("invalid deployment spec: model_version_id, template_version_id, desired_replicas>=1 required")
	}
	return nil
}
```

- [ ] **Step 5: Implement MockComputeProvider**

Create `go-server/internal/runtime/mock.go`:

```go
package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// MockComputeProvider is a dev/unit-test compute provider that tracks a
// workload ref per deployment in memory (spec §3.4).
type MockComputeProvider struct {
	mu        sync.Mutex
	workloads map[string]string // deployment ID -> workload ref
}

func NewMockComputeProvider() *MockComputeProvider {
	return &MockComputeProvider{workloads: map[string]string{}}
}

func (m *MockComputeProvider) RequestCapacity(ctx context.Context, d *controlplane.Deployment) (string, error) {
	ref := "mock-wl-" + d.ID
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workloads[d.ID] = ref
	return ref, nil
}

func (m *MockComputeProvider) ReleaseCapacity(ctx context.Context, d *controlplane.Deployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workloads, d.ID)
	return nil
}

func (m *MockComputeProvider) GetWorkloadStatus(ctx context.Context, d *controlplane.Deployment) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workloads[d.ID]; !ok {
		return "", fmt.Errorf("no workload for deployment %s", d.ID)
	}
	return "RUNNING", nil
}

func (m *MockComputeProvider) UpdateWorkload(ctx context.Context, d *controlplane.Deployment) error {
	return nil
}
```

- [ ] **Step 6: Run to verify they pass**

Run: `cd go-server && go test ./internal/runtime/ -v`
Expected: PASS (3 tests).

- [ ] **Step 7: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add go-server/internal/runtime
git commit -m "feat(runtime): ServingRuntimeAdapter + WorkerAdapter + MockComputeProvider"
```

---

### Task 3: Migration 0002 (workload_ref) + controlplane methods

**Files:**
- Create: `go-server/internal/db/migrations/0002_deployment_workload_ref.sql`
- Modify: `go-server/internal/controlplane/deployment.go`
- Create: `go-server/internal/controlplane/deployment_test.go`

**Interfaces:**
- Consumes: `controlplane.Service` (existing `CreateDeployment`/`GetDeployment`/`ListDeployments`/`TransitionDeployment`/`CreateRevision`).
- Produces (used by Task 4 + Task 5):
  - `Deployment.WorkloadRef string` field (`json:"workload_ref,omitempty"`).
  - `type Endpoint struct { ID string; DeploymentID string; Path string; Protocol string; Status string; CreatedAt time.Time }` (`json:"..."` tags).
  - `func (s *Service) SetWorkloadRef(ctx context.Context, id, ref string) error`
  - `func (s *Service) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*Endpoint, error)`

- [ ] **Step 1: Write the migration**

Create `go-server/internal/db/migrations/0002_deployment_workload_ref.sql`:

```sql
-- +goose Up
ALTER TABLE deployments ADD COLUMN workload_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE deployments DROP COLUMN workload_ref;
```

- [ ] **Step 2: Write the failing integration test**

Create `go-server/internal/controlplane/deployment_test.go`:

```go
package controlplane

import (
	"context"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/db"
)

// TestWorkloadRefAndEndpointIntegration runs against a live Postgres
// (set AI_FACTORY_DATABASE_URL), mirroring users_test.go.
func TestWorkloadRefAndEndpointIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(d.Pool())

	// Use an ephemeral tenant so re-runs never collide on FK constraints.
	tenant, err := s.CreateTenant(ctx, "wl-test")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })

	deploy, err := s.CreateDeployment(ctx, Deployment{
		TenantID: tenant.ID, ModelVersionID: "mv-x", TemplateVersionID: "tv-x",
		Name: "svc", Region: "us-east-1", DesiredReplicas: 1,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if err := s.SetWorkloadRef(ctx, deploy.ID, "mock-wl-abc"); err != nil {
		t.Fatalf("set workload ref: %v", err)
	}
	got, err := s.GetDeployment(ctx, deploy.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.WorkloadRef != "mock-wl-abc" {
		t.Fatalf("WorkloadRef = %q, want mock-wl-abc", got.WorkloadRef)
	}

	ep, err := s.CreateEndpoint(ctx, deploy.ID, "/v1/chat/completions/"+deploy.ID, "openai")
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if ep.DeploymentID != deploy.ID || ep.Path == "" || ep.Protocol != "openai" {
		t.Fatalf("endpoint = %+v", ep)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd go-server && go test ./internal/controlplane/ -run TestWorkloadRefAndEndpointIntegration -v`
Expected: FAIL — `undefined: SetWorkloadRef` / `undefined: CreateEndpoint` (or a migration error if the column is absent). With no `AI_FACTORY_DATABASE_URL`, it SKIPs — that is expected too, but compile-time failure still surfaces first.

- [ ] **Step 4: Implement the field, Endpoint type, and methods**

Modify `go-server/internal/controlplane/deployment.go`:

Add `WorkloadRef` to the `Deployment` struct:

```go
type Deployment struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id"`
	ModelVersionID    string    `json:"model_version_id"`
	TemplateVersionID string    `json:"template_version_id"`
	Name              string    `json:"name"`
	Region            string    `json:"region"`
	DesiredReplicas   int       `json:"desired_replicas"`
	Status            string    `json:"status"`
	WorkloadRef       string    `json:"workload_ref,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
```

Add the `Endpoint` type (after `DeploymentRevision`):

```go
// Endpoint is the routable serving address created when a deployment reaches
// READY (spec §5 routing).
type Endpoint struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deployment_id"`
	Path         string    `json:"path"`
	Protocol     string    `json:"protocol"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}
```

Update `GetDeployment` and `ListDeployments` SELECTs and scans to include `workload_ref` between `status` and `created_at`. The queries become:

```go
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, workload_ref, created_at, updated_at
		 FROM deployments WHERE id = $1`, id).
		Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.WorkloadRef, &d.CreatedAt, &d.UpdatedAt)
```

```go
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, workload_ref, created_at, updated_at
		 FROM deployments WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
```

and in the `ListDeployments` scan add `&d.WorkloadRef` between `&d.Status` and `&d.CreatedAt`.

Add the two methods at the end of the file (before `nullableUUID`):

```go
// SetWorkloadRef records the compute reference bound to a deployment by its
// ComputeProvider.
func (s *Service) SetWorkloadRef(ctx context.Context, id, ref string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE deployments SET workload_ref = $1, updated_at = now() WHERE id = $2`,
		ref, id)
	if err != nil {
		return fmt.Errorf("set workload ref: %w", err)
	}
	return nil
}

// CreateEndpoint records a routable endpoint for a READY deployment.
func (s *Service) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*Endpoint, error) {
	e := &Endpoint{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Path: path, Protocol: protocol, Status: "ACTIVE",
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO endpoints (id, deployment_id, path, protocol, status)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		e.ID, e.DeploymentID, e.Path, e.Protocol, e.Status).Scan(&e.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create endpoint: %w", err)
	}
	return e, nil
}
```

- [ ] **Step 5: Confirm the endpoints table already exists**

`0001_init.sql` already creates `endpoints` (lines 119-126) with columns `id, deployment_id, path, protocol, status, created_at` — no migration needed. Just verify:

Run: `grep -n "CREATE TABLE endpoints" go-server/internal/db/migrations/0001_init.sql`
Expected: a match at line 119. (If it is ever missing, add the table to a new `0003_*.sql`.)

- [ ] **Step 6: Run tests**

Run: `cd go-server && go test ./internal/controlplane/ ./internal/db/ ./internal/events/ -v`
Expected: integration test PASSES when `AI_FACTORY_DATABASE_URL` is set; otherwise SKIPs; no compile errors anywhere.

- [ ] **Step 7: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add go-server/internal/db/migrations/0002_deployment_workload_ref.sql go-server/internal/controlplane/deployment.go go-server/internal/controlplane/deployment_test.go
git commit -m "feat(controlplane): workload_ref column + SetWorkloadRef + CreateEndpoint"
```

---

### Task 4: Deployment worker (async orchestration)

**Files:**
- Create: `go-server/internal/runtime/worker.go`
- Create: `go-server/internal/runtime/worker_test.go`

**Interfaces:**
- Consumes: `events` (Task 1 — `TopicDeploymentEvents`, `NewEvent`, `TypeDeploymentCreated/Ready/Failed/StopRequested/Stopped`, `Producer`, `Consumer`), `runtime` interfaces (Task 2), `controlplane` methods (Task 3).
- Produces (used by Task 5):
  - `type DeploymentStore interface { GetDeployment(ctx, id string) (*controlplane.Deployment, error); TransitionDeployment(ctx, id, to string) (*controlplane.Deployment, error); CreateRevision(ctx, deploymentID string, spec map[string]any, createdBy string) (*controlplane.DeploymentRevision, error); SetWorkloadRef(ctx, id, ref string) error; CreateEndpoint(ctx, deploymentID, path, protocol string) (*controlplane.Endpoint, error) }`
  - `*controlplane.Service` satisfies `DeploymentStore` (all methods exist after Task 3).
  - `func NewWorker(store DeploymentStore, adapter ServingRuntimeAdapter, compute ComputeProvider, producer events.Producer, consumer events.Consumer, log *slog.Logger) *Worker`
  - `func (w *Worker) Run(ctx context.Context) error` — subscribes to the deployment topic (non-blocking; returns after Subscribe) and returns the subscribe error.
  - `func (w *Worker) handle(ctx context.Context, ev events.Event) error` — exported for tests.

- [ ] **Step 1: Write the failing tests**

Create `go-server/internal/runtime/worker_test.go`:

```go
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
)

type fakeStore struct {
	mu          sync.Mutex
	deployments map[string]*controlplane.Deployment
	workloadRef string
	endpoints   []*controlplane.Endpoint
	revSpecs    []map[string]any
}

func newFakeStore() *fakeStore {
	return &fakeStore{deployments: map[string]*controlplane.Deployment{}}
}

func (f *fakeStore) GetDeployment(ctx context.Context, id string) (*controlplane.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	cp := *d
	return &cp, nil
}

func (f *fakeStore) TransitionDeployment(ctx context.Context, id, to string) (*controlplane.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return nil, fmt.Errorf("deployment %s not found", id)
	}
	d.Status = to
	cp := *d
	return &cp, nil
}

func (f *fakeStore) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*controlplane.DeploymentRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revSpecs = append(f.revSpecs, spec)
	return &controlplane.DeploymentRevision{
		ID: "rev-1", DeploymentID: deploymentID, Revision: 1,
		Spec: spec, CreatedBy: createdBy,
	}, nil
}

func (f *fakeStore) SetWorkloadRef(ctx context.Context, id, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workloadRef = ref
	return nil
}

func (f *fakeStore) CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*controlplane.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := &controlplane.Endpoint{ID: "ep-1", DeploymentID: deploymentID, Path: path, Protocol: protocol, Status: "ACTIVE"}
	f.endpoints = append(f.endpoints, e)
	return e, nil
}

// failAdapter fails Start to exercise the FAILED path.
type failAdapter struct{ *WorkerAdapter }

func (a failAdapter) Start(ctx context.Context, d *controlplane.Deployment) error {
	return errors.New("worker exploded")
}

func newTestWorker(store DeploymentStore, adapter ServingRuntimeAdapter, bus *events.MemoryEventBus) *Worker {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorker(store, adapter, NewMockComputeProvider(), bus, bus, log)
}

func TestWorkerOnCreatedHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", map[string]any{"created_by": "u1"})
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentReady {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentReady)
	}
	if store.workloadRef == "" {
		t.Fatal("workload_ref not set")
	}
	if len(store.endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(store.endpoints))
	}
	if len(store.revSpecs) != 1 {
		t.Fatalf("revisions = %d, want 1", len(store.revSpecs))
	}
	ready := false
	for _, e := range published {
		if e.Type == events.TypeDeploymentReady {
			ready = true
		}
	}
	if !ready {
		t.Fatal("no deployment_ready event published")
	}
}

func TestWorkerOnCreatedIdempotent(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = controlplane.DeploymentReady

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.revSpecs) != 0 {
		t.Fatalf("revisions = %d, want 0 (already READY, must not reprocess)", len(store.revSpecs))
	}
	if len(store.endpoints) != 0 {
		t.Fatalf("endpoints = %d, want 0", len(store.endpoints))
	}
}

func TestWorkerOnCreatedAdapterFailure(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, failAdapter{NewWorkerAdapter("localhost:1")}, bus)

	ev := events.NewEvent(events.TypeDeploymentCreated, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentFailed {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentFailed)
	}
	failed := false
	for _, e := range published {
		if e.Type == events.TypeDeploymentFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no deployment_failed event published")
	}
}

func TestWorkerOnStopHappyPath(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = controlplane.DeploymentReady

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(context.Background(), events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentStopped {
		t.Fatalf("status = %s, want %s", got, controlplane.DeploymentStopped)
	}
	stopped := false
	for _, e := range published {
		if e.Type == events.TypeDeploymentStopped {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no deployment_stopped event published")
	}
}

func TestWorkerOnStopAlreadyStopped(t *testing.T) {
	store := newFakeStore()
	store.deployments["d1"] = validDeployment()
	store.deployments["d1"].Status = controlplane.DeploymentStopped

	bus := events.NewMemoryEventBus()
	w := newTestWorker(store, NewWorkerAdapter("localhost:1"), bus)

	ev := events.NewEvent(events.TypeDeploymentStopRequested, "t1", "d1", nil)
	if err := w.handle(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.deployments["d1"].Status; got != controlplane.DeploymentStopped {
		t.Fatalf("status = %s, want %s (unchanged)", got, controlplane.DeploymentStopped)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd go-server && go test ./internal/runtime/ -run 'TestWorker' -v`
Expected: FAIL — `undefined: NewWorker`.

- [ ] **Step 3: Implement the worker**

Create `go-server/internal/runtime/worker.go`:

```go
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/events"
)

// DeploymentStore is the slice of the control plane the worker needs. It is
// satisfied by *controlplane.Service (all methods exist after Task 3).
type DeploymentStore interface {
	GetDeployment(ctx context.Context, id string) (*controlplane.Deployment, error)
	TransitionDeployment(ctx context.Context, id, to string) (*controlplane.Deployment, error)
	CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*controlplane.DeploymentRevision, error)
	SetWorkloadRef(ctx context.Context, id, ref string) error
	CreateEndpoint(ctx context.Context, deploymentID, path, protocol string) (*controlplane.Endpoint, error)
}

// Worker consumes deployment events and drives the deployment state machine
// asynchronously: deployment_created -> PENDING..READY, deployment_stop_requested
// -> STOPPED. Idempotent by construction: events whose deployment is already
// in flight/ready/stopped/failed are no-ops (the state machine guard).
type Worker struct {
	store    DeploymentStore
	adapter  ServingRuntimeAdapter
	compute  ComputeProvider
	producer events.Producer
	consumer events.Consumer
	log      *slog.Logger
}

func NewWorker(store DeploymentStore, adapter ServingRuntimeAdapter, compute ComputeProvider, producer events.Producer, consumer events.Consumer, log *slog.Logger) *Worker {
	return &Worker{store: store, adapter: adapter, compute: compute, producer: producer, consumer: consumer, log: log}
}

// Run subscribes the worker to the deployment topic. Subscribe is non-blocking
// (handlers run on the bus's goroutine), so Run returns the subscribe error or
// nil immediately.
func (w *Worker) Run(ctx context.Context) error {
	return w.consumer.Subscribe(ctx, events.TopicDeploymentEvents, w.handle)
}

// handle routes deployment-topic events; unknown types are ignored.
func (w *Worker) handle(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.TypeDeploymentCreated:
		return w.onCreated(ctx, ev)
	case events.TypeDeploymentStopRequested:
		return w.onStop(ctx, ev)
	default:
		return nil
	}
}

func (w *Worker) onCreated(ctx context.Context, ev events.Event) error {
	d, err := w.store.GetDeployment(ctx, ev.ResourceID)
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", ev.ResourceID, err)
	}

	// Idempotency guard: already in flight / ready / failed => nothing to do.
	switch d.Status {
	case controlplane.DeploymentProvisioning, controlplane.DeploymentStarting, controlplane.DeploymentReady, controlplane.DeploymentFailed:
		w.log.Info("deployment already handled", "id", d.ID, "status", d.Status)
		return nil
	case controlplane.DeploymentStopped:
		// Restart: STOPPED -> PENDING, then proceed from PENDING below.
		if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentPending); err != nil {
			return fmt.Errorf("restart to pending: %w", err)
		}
	}

	createdBy, _ := ev.Payload["created_by"].(string)
	if _, err := w.store.CreateRevision(ctx, d.ID, specMap(d), createdBy); err != nil {
		return fmt.Errorf("create revision: %w", err)
	}

	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentProvisioning); err != nil {
		return w.fail(ctx, d, err)
	}

	ref, err := w.compute.RequestCapacity(ctx, d)
	if err != nil {
		return w.fail(ctx, d, fmt.Errorf("request capacity: %w", err))
	}
	if err := w.store.SetWorkloadRef(ctx, d.ID, ref); err != nil {
		return fmt.Errorf("set workload ref: %w", err)
	}

	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStarting); err != nil {
		return w.fail(ctx, d, err)
	}

	if err := w.adapter.Start(ctx, d); err != nil {
		return w.fail(ctx, d, fmt.Errorf("adapter start: %w", err))
	}

	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentReady); err != nil {
		return w.fail(ctx, d, err)
	}

	if _, err := w.store.CreateEndpoint(ctx, d.ID, endpointPath(d.ID), "openai"); err != nil {
		w.log.Warn("endpoint not created", "deployment", d.ID, "err", err)
	}

	w.log.Info("deployment ready", "id", d.ID, "workload_ref", ref)
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentReady, d.TenantID, d.ID, map[string]any{"workload_ref": ref}))
}

func (w *Worker) onStop(ctx context.Context, ev events.Event) error {
	d, err := w.store.GetDeployment(ctx, ev.ResourceID)
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", ev.ResourceID, err)
	}
	if d.Status == controlplane.DeploymentStopped || d.Status == controlplane.DeploymentStopping {
		w.log.Info("deployment already stopped/stopping", "id", d.ID, "status", d.Status)
		return nil
	}
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStopping); err != nil {
		return fmt.Errorf("transition to stopping: %w", err)
	}
	if err := w.adapter.Stop(ctx, d); err != nil {
		return w.fail(ctx, d, fmt.Errorf("adapter stop: %w", err))
	}
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentStopped); err != nil {
		return w.fail(ctx, d, err)
	}
	if err := w.compute.ReleaseCapacity(ctx, d); err != nil {
		w.log.Warn("release capacity", "deployment", d.ID, "err", err)
	}
	w.log.Info("deployment stopped", "id", d.ID)
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentStopped, d.TenantID, d.ID, nil))
}

// fail transitions to FAILED (terminal) and publishes deployment_failed.
func (w *Worker) fail(ctx context.Context, d *controlplane.Deployment, cause error) error {
	w.log.Error("deployment failed", "id", d.ID, "cause", cause)
	if _, err := w.store.TransitionDeployment(ctx, d.ID, controlplane.DeploymentFailed); err != nil &&
		!errors.Is(err, controlplane.ErrInvalidTransition) {
		return fmt.Errorf("mark failed: %w", err)
	}
	return w.producer.Publish(ctx, events.TopicDeploymentEvents, events.NewEvent(
		events.TypeDeploymentFailed, d.TenantID, d.ID, map[string]any{"error": cause.Error()}))
}

// specMap captures the deployment spec for a revision record.
func specMap(d *controlplane.Deployment) map[string]any {
	return map[string]any{
		"name":                d.Name,
		"region":              d.Region,
		"desired_replicas":    d.DesiredReplicas,
		"model_version_id":    d.ModelVersionID,
		"template_version_id": d.TemplateVersionID,
	}
}

// endpointPath is the placeholder routable path for a READY deployment
// (spec §5 routing; a real inference gateway resolves this in M3).
func endpointPath(deploymentID string) string {
	return "/v1/chat/completions/" + deploymentID
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd go-server && go test ./internal/runtime/ -v`
Expected: PASS — Task 2's 3 tests + Task 4's 5 tests.

- [ ] **Step 5: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add go-server/internal/runtime/worker.go go-server/internal/runtime/worker_test.go
git commit -m "feat(runtime): deployment worker — async PENDING->READY via events"
```

---

### Task 5: Wire API + main + E2E

**Files:**
- Modify: `go-server/internal/api/controlplane.go`
- Modify: `go-server/internal/api/controlplane_test.go`
- Modify: `go-server/cmd/server/main.go`
- Modify: `deployments/docker-compose.yml`

**Interfaces:**
- Consumes: `events.NewMemoryEventBus` (Task 1), `runtime.NewWorker/NewWorkerAdapter/NewMockComputeProvider` (Task 2 + Task 4), `controlplane` methods (Task 3).
- Produces (used by Task 6): `POST /api/v1/deployments` → 202 with `deployment_created` published; `POST /api/v1/deployments/{id}/start` → 202 + `deployment_created`; `.../{id}/stop` → 202 + `deployment_stop_requested`; unknown action → 400.

- [ ] **Step 1: Write the failing handler tests**

Add to `go-server/internal/api/controlplane_test.go`:

```go
import (
	...
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/runtime"
)

// TestCreateDeploymentPublishesEvent asserts POST /deployments returns 202 and
// publishes deployment_created on the bus.
func TestCreateDeploymentPublishesEvent(t *testing.T) {
	ctx := context.Background()
	d := dbConnOrSkip(t) // helper below
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	tenant, _ := cp.CreateTenant(ctx, "pub-ev-"+uuid.NewString()[:8])
	hash, _ := auth.HashPassword("admin-pass")
	user, _ := cp.CreateUser(ctx, "u-"+uuid.NewString()[:8], "u@io", hash, auth.RoleTenantAdmin, tenant.ID)
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := events.NewMemoryEventBus()
	var published []events.Event
	if err := bus.Subscribe(ctx, events.TopicDeploymentEvents,
		func(ctx context.Context, ev events.Event) error { published = append(published, ev); return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	h := NewControlPlaneHandler(cp, authSvc, secret, bus)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	// deployments has FKs to model_versions / serving_template_versions, so
	// create real catalog rows first.
	model, err := cp.CreateModel(ctx, controlplane.Model{Name: "qwen-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := cp.CreateModelVersion(ctx, controlplane.ModelVersion{ModelID: model.ID, Version: "1.0", ArtifactURI: "file:///m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := cp.CreateTemplate(ctx, controlplane.ServingTemplate{Name: "tpl-" + uuid.NewString()[:8], Runtime: "python"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := cp.CreateTemplateVersion(ctx, controlplane.TemplateVersion{TemplateID: tpl.ID, Version: "1.0", Image: "img"})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model_version_id": mv.ID, "template_version_id": tv.ID,
		"name": "svc", "region": "us-east-1", "desired_replicas": 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create deployment code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if len(published) != 1 || published[0].Type != events.TypeDeploymentCreated {
		t.Fatalf("published = %+v, want exactly one deployment_created", published)
	}
}
```

The helpers the test needs (add below `TestLoginE2E` in the same file):

```go
func dbConnOrSkip(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func loginHelper(t *testing.T, mux *http.ServeMux, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal login: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("empty access_token")
	}
	return resp.AccessToken
}
```

(Refactor `TestLoginE2E` to reuse `dbConnOrSkip`/`loginHelper` if you wish, but the only required edit there is the constructor call in Step 3.)

- [ ] **Step 2: Run to verify they fail**

Run: `cd go-server && go test ./internal/api/ -run TestCreateDeploymentPublishesEvent -v`
Expected: FAIL — `too many arguments in call to NewControlPlaneHandler`.

- [ ] **Step 3: Update the handler to take a Producer and publish events**

Modify `go-server/internal/api/controlplane.go`:

```go
import (
	...
	"github.com/ai-factory/go-server/internal/events"
)

type ControlPlaneHandler struct {
	cp       *controlplane.Service
	auth     *auth.Service
	secret   []byte
	producer events.Producer
}

func NewControlPlaneHandler(cp *controlplane.Service, authSvc *auth.Service, secret []byte, producer events.Producer) *ControlPlaneHandler {
	return &ControlPlaneHandler{cp: cp, auth: authSvc, secret: secret, producer: producer}
}
```

Replace `handleCreateDeployment` with a version that publishes `deployment_created`:

```go
func (h *ControlPlaneHandler) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var d controlplane.Deployment
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	d.TenantID = claims.TenantID // derive tenant from auth, never trust body
	created, err := h.cp.CreateDeployment(r.Context(), d)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	ev := events.NewEvent(events.TypeDeploymentCreated, claims.TenantID, created.ID, map[string]any{
		"name":               created.Name,
		"region":             created.Region,
		"desired_replicas":   created.DesiredReplicas,
		"model_version_id":   created.ModelVersionID,
		"template_version_id": created.TemplateVersionID,
		"created_by":         claims.UserID,
	})
	if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
		// The deployment is persisted but not queued: surface it loudly.
		writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", "deployment persisted but event publish failed")
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}
```

Replace `handleDeploymentAction` with an event-publishing version:

```go
func (h *ControlPlaneHandler) handleDeploymentAction(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "bad path")
		return
	}
	id, action := parts[0], parts[1]
	d, err := h.cp.GetDeployment(r.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	// Async: the worker performs the actual state transitions.
	switch action {
	case "start":
		ev := events.NewEvent(events.TypeDeploymentCreated, claims.TenantID, id, map[string]any{
			"name": d.Name, "region": d.Region, "desired_replicas": d.DesiredReplicas,
			"model_version_id": d.ModelVersionID, "template_version_id": d.TemplateVersionID,
			"created_by": claims.UserID,
		})
		if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, d)
	case "stop":
		ev := events.NewEvent(events.TypeDeploymentStopRequested, claims.TenantID, id, nil)
		if err := h.producer.Publish(r.Context(), events.TopicDeploymentEvents, ev); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, d)
	default:
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "unknown action "+action)
		return
	}
}
```

Update `TestLoginE2E` (line ~64) constructor call:

```go
	h := NewControlPlaneHandler(cp, authSvc, secret, events.NewMemoryEventBus())
```

- [ ] **Step 4: Run the API tests**

Run: `cd go-server && go test ./internal/api/ -run 'TestLoginE2E|TestCreateDeploymentPublishesEvent' -v`
Expected: PASS (E2E runs only with `AI_FACTORY_DATABASE_URL` set; the event test likewise).

- [ ] **Step 5: Write the async-deploy E2E**

Add to `go-server/internal/api/controlplane_test.go`:

```go
// TestAsyncDeployE2E drives the full M2 path: create deployment via HTTP -> the
// memory bus synchronously invokes the worker -> deployment reaches READY.
func TestAsyncDeployE2E(t *testing.T) {
	d := dbConnOrSkip(t)
	ctx := context.Background()
	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	suffix := uuid.NewString()[:8]
	tenant, err := cp.CreateTenant(ctx, "async-"+suffix)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	hash, _ := auth.HashPassword("admin-pass")
	user, err := cp.CreateUser(ctx, "admin-"+suffix, "admin-"+suffix+"@io", hash, auth.RoleTenantAdmin, tenant.ID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })

	bus := events.NewMemoryEventBus()
	worker := runtime.NewWorker(cp, runtime.NewWorkerAdapter("localhost:1"), runtime.NewMockComputeProvider(), bus, bus,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := worker.Run(ctx); err != nil { // Subscribe is non-blocking
		t.Fatalf("worker run: %v", err)
	}

	h := NewControlPlaneHandler(cp, authSvc, secret, bus)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token := loginHelper(t, mux, user.Username, "admin-pass")

	post := func(path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("POST %s code = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return out
	}
	get := func(path string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s code = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return out
	}

	model := post("/api/v1/models", map[string]any{"name": "qwen-" + suffix, "task": "text-generation", "framework": "transformers"}, http.StatusCreated)
	mv := post("/api/v1/models/"+model["id"].(string)+"/versions",
		map[string]any{"version": "1.0", "artifact_uri": "file:///m"}, http.StatusCreated)
	tpl := post("/api/v1/templates", map[string]any{"name": "tpl-" + suffix, "runtime": "python"}, http.StatusCreated)
	tv := post("/api/v1/templates/"+tpl["id"].(string)+"/versions",
		map[string]any{"version": "1.0", "image": "ai-factory:latest"}, http.StatusCreated)

	dep := post("/api/v1/deployments", map[string]any{
		"model_version_id": mv["id"].(string),
		"template_version_id": tv["id"].(string),
		"name": "svc-" + suffix, "region": "us-east-1", "desired_replicas": 1,
	}, http.StatusAccepted)
	depID := dep["id"].(string)

	// Memory bus dispatch is synchronous: the worker finished before 202 returned.
	got := get("/api/v1/deployments/"+depID, http.StatusOK)
	if got["status"] != "READY" {
		t.Fatalf("deployment status = %v, want READY", got["status"])
	}
	if got["workload_ref"] == "" {
		t.Fatal("deployment workload_ref empty, want mock ref")
	}
}
```

Add imports to `controlplane_test.go`: `io`, `log/slog`, `github.com/ai-factory/go-server/internal/events`, `github.com/ai-factory/go-server/internal/runtime`.

- [ ] **Step 6: Run the full API test suite**

Run: `cd go-server && go test ./internal/api/ -v`
Expected: PASS (with `AI_FACTORY_DATABASE_URL` set: `TestLoginE2E`, `TestCreateDeploymentPublishesEvent`, `TestAsyncDeployE2E` all pass; without it they skip).

- [ ] **Step 7: Wire main.go**

Modify `go-server/cmd/server/main.go`:

Add imports:

```go
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/runtime"
```

Move the handler construction to AFTER the Kafka block. Replace lines 60 (the `cph := ...` before `seedAdmin`) with nothing, and after `seedAdmin` insert:

```go
	// Events bus (Kafka) — optional. The deployment worker needs it, but the
	// chat/inference path must boot without it: fall back to an in-memory bus
	// (deployments stay PENDING) and warn.
	bus, kafkaErr := events.NewKafkaEventBus(cfg.KafkaAddr)
	if kafkaErr != nil {
		log.Printf("WARN: kafka unreachable at %s — deployment worker disabled (%v)", cfg.KafkaAddr, kafkaErr)
		bus = events.NewMemoryEventBus()
	} else {
		defer bus.Close()
		log.Printf("Kafka event bus connected: %s", cfg.KafkaAddr)
		worker := runtime.NewWorker(cp, runtime.NewWorkerAdapter(*inferenceAddr), runtime.NewMockComputeProvider(), bus, bus, slog.Default())
		if err := worker.Run(ctx); err != nil {
			log.Fatalf("deployment worker: %v", err)
		}
		log.Println("Deployment worker started (async deploy)")
	}
	cph := api.NewControlPlaneHandler(cp, authSvc, []byte(cfg.JWTSecret), bus)
```

Add the `log/slog` import (currently only `log` is imported) and keep the rest of `main` unchanged (the `cph` variable is used at line 108 `cph.RegisterRoutes(mux)`).

- [ ] **Step 8: Build + vet**

Run: `cd go-server && go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 9: Compose env note**

Modify `deployments/docker-compose.yml` — add the kafka env var to the `server` service environment (with a comment that the container currently can't reach the compose kafka's `localhost` advertised listener; the server tolerates this by warning + falling back):

```yaml
    environment:
      AI_FACTORY_DATABASE_URL: postgres://ai_factory:ai_factory@postgres:5432/ai_factory?sslmode=disable
      AI_FACTORY_JWT_SECRET: ${AI_FACTORY_JWT_SECRET:-dev-secret-change-me-0123456789}
      AI_FACTORY_LOG_LEVEL: info
      # NOTE: kafka advertises localhost:9092 (host-only), so the containerized
      # server cannot reach it; it warns and runs with a memory event bus
      # (deployments stay PENDING). Async deploy in Docker needs a dual
      # advertised listener — out of scope for M2.
      AI_FACTORY_KAFKA_ADDR: kafka:9092
```

- [ ] **Step 10: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add go-server/internal/api go-server/cmd/server/main.go deployments/docker-compose.yml
git commit -m "feat(api): async deploy — publish events, start worker, wire kafka"
```

---

### Task 6: Demo script + docs

**Files:**
- Create: `scripts/m2-demo.sh`
- Modify: `CLAUDE.md`
- Modify: `docs/TRACKING.md`

**Interfaces:**
- Consumes: the wired server from Task 5. Requires the seeded `admin` / `admin1234` (`cmd/server/seed.go` seeds tenant `acme` + admin).

- [ ] **Step 1: Write the demo script**

Create `scripts/m2-demo.sh`:

```bash
#!/usr/bin/env bash
# M2 demo: async deployment — POST /deployments -> 202 -> PENDING..READY.
# Requires: Go server + Postgres + Kafka running, jq installed.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
KAFKA_ADDR="${AI_FACTORY_KAFKA_ADDR:-localhost:9092}"
TOKEN=""

echo "==> 1. Kafka reachable?"
if ! (echo > "/dev/tcp/${KAFKA_ADDR/:/\/}") 2>/dev/null; then
  echo "Kafka not reachable at $KAFKA_ADDR. Start it:"
  echo "  docker compose -f deployments/docker-compose.yml up -d postgres kafka"
  exit 1
fi
echo "    OK ($KAFKA_ADDR)"

echo "==> 2. Server healthy?"
curl -fsS "$BASE/health" >/dev/null && echo "    OK ($BASE)"

echo "==> 3. Login (seeded admin/admin1234)"
LOGIN=$(curl -fsS -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin1234"}')
TOKEN=$(echo "$LOGIN" | jq -r .access_token)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || { echo "login failed"; exit 1; }

AUTH=(-H "Authorization: Bearer $TOKEN")

suffix=$(date +%s)

echo "==> 4. Create model + version, template + version"
MODEL=$(curl -fsS -X POST "$BASE/api/v1/models" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"qwen-m2-$suffix\",\"task\":\"text-generation\",\"framework\":\"transformers\"}")
MV=$(curl -fsS -X POST "$BASE/api/v1/models/$(echo "$MODEL" | jq -r .id)/versions" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d '{"version":"1.0","artifact_uri":"file:///models/qwen.gguf"}')
TPL=$(curl -fsS -X POST "$BASE/api/v1/templates" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"tpl-m2-$suffix\",\"runtime\":\"python\"}")
TV=$(curl -fsS -X POST "$BASE/api/v1/templates/$(echo "$TPL" | jq -r .id)/versions" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d '{"version":"1.0","image":"ai-factory:latest"}')
echo "    model_version=$(echo "$MV" | jq -r .id) template_version=$(echo "$TV" | jq -r .id)"

echo "==> 5. Create deployment -> 202 Accepted"
DEP=$(curl -fsS -X POST "$BASE/api/v1/deployments" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"model_version_id\":\"$(echo "$MV" | jq -r .id)\",\"template_version_id\":\"$(echo "$TV" | jq -r .id)\",\"name\":\"svc-m2-$suffix\",\"region\":\"us-east-1\",\"desired_replicas\":1}")
DEP_ID=$(echo "$DEP" | jq -r .id)
echo "    deployment_id=$DEP_ID status=$(echo "$DEP" | jq -r .status)"

echo "==> 6. Poll until READY (max 15s)"
for i in $(seq 1 15); do
  STATUS=$(curl -fsS "$BASE/api/v1/deployments/$DEP_ID" "${AUTH[@]}" | jq -r .status)
  echo "    t=${i}s status=$STATUS"
  [ "$STATUS" = "READY" ] && break
  [ "$STATUS" = "FAILED" ] && { echo "    deployment FAILED"; exit 1; }
  sleep 1
done

echo "==> 7. Final deployment"
curl -fsS "$BASE/api/v1/deployments/$DEP_ID" "${AUTH[@]}" | jq '{id, status, workload_ref, region, desired_replicas}'
echo "DONE."
```

- [ ] **Step 2: Make it executable**

Run (Git Bash / POSIX):

```bash
chmod +x scripts/m2-demo.sh
```

- [ ] **Step 3: Update CLAUDE.md**

In `CLAUDE.md`, under the architecture ASCII diagram, add to the Go server box a line for the new components, e.g. after the existing `BatchScheduler`/`Tool executor` lines:

```
                         ├── Events bus (Kafka: serving.deployment.events)
                         ├── Deployment worker (async PENDING→READY via ServingRuntimeAdapter)
```

And in the **Running** section, under the `docker compose` note for Postgres, add Kafka:

```text
# NOTE: the server requires Postgres (control plane) and fails at boot if the DB is
# unreachable. Start it first if not already running:
#   docker compose -f deployments/docker-compose.yml up -d postgres kafka
# Kafka is optional (only the deployment worker needs it): if unreachable the server
# warns and runs with an in-memory event bus (deployments stay PENDING).
# The DB URL comes from AI_FACTORY_DATABASE_URL (default: local dev compose).
```

And under **Development** add one line for the M2 demo:

```bash
# M2 async-deploy demo (server + postgres + kafka up; requires jq)
bash scripts/m2-demo.sh
```

- [ ] **Step 4: Update TRACKING.md**

In `docs/TRACKING.md`, mark the M2 milestone done (or add a row to the platform roadmap table), e.g.:

```markdown
| M2 — Runtime adapter + async deploy | ServingRuntimeAdapter + MockComputeProvider + Kafka events + deployment worker | ✅ Done |
```

Match the table style already in the file; if there is no platform table, add a short "M2 (async deploy) done" line to the progress tracker section.

- [ ] **Step 5: Run full Go test suite**

Run: `cd go-server && go test ./... -count=1`
Expected: all tests pass (integration/E2E skip without `AI_FACTORY_DATABASE_URL`).

- [ ] **Step 6: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add scripts/m2-demo.sh CLAUDE.md docs/TRACKING.md
git commit -m "docs: M2 demo script + roadmap update"
```

---

## Self-Review

**1. Spec coverage (spec `docs/superpowers/specs/2026-08-15-serving-platform-design.md`):**
- §3.3 ServingRuntimeAdapter — Task 2 (`runtime.go`).
- §3.4 ComputeProvider + MockComputeProvider — Task 2 (`runtime.go`, `mock.go`).
- §6.1 topics / §6.2 envelope — Task 1 (`events.go`, exact strings + JSON names).
- §5 routing → endpoint on READY — Task 3 (`CreateEndpoint`) + Task 4 (`endpointPath`).
- M2 milestone (§12) async deploy: POST → 202 → PENDING→READY — Tasks 4 + 5 (+ E2E).
- **Deliberate deviations (flagged for the human):** (a) Kafka optional at boot (not hard-required like Postgres) so the chat path never depends on infra; (b) `WorkerAdapter` has no real worker deployment RPC — lifecycle ops validate the spec and the worker reports control-plane status (documented M2 boundary; spec §3.3's real provisioning lands with a future worker API); (c) `ComputeProvider` full 4-method interface is implemented but only `RequestCapacity`/`ReleaseCapacity` are exercised in M2.
- **Known M2 gap (documented, not fixed):** dockerized server cannot reach compose kafka's host-only advertised listener; it warns and uses the memory fallback. Async deploy is verified via the host path (demo script + E2E with MemoryEventBus) and the gated Kafka round-trip test.

**2. Placeholder scan:** every step carries concrete code and exact run commands; no TBD/TODO. The only conditional step is Task 3 Step 5 (the `endpoints` table may already exist in migration 0001 — the check makes it deterministic).

**3. Type consistency:** `NewEvent(eventType, tenantID, resourceID string, payload map[string]any)` is used identically in Tasks 4 and 5. `NewWorker(store, adapter, compute, producer, consumer, log)` matches its single construction site in `main.go` and tests. `DeploymentStore` method signatures match `*controlplane.Service` after Task 3. `handleDeploymentAction` publishes `TypeDeploymentCreated` on `start` (matching the worker's guard, which also handles restart from `STOPPED`).
