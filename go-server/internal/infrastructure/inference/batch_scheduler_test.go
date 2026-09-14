package inference

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	pb "github.com/ai-factory/go-server/internal/infrastructure/inference/pb"
)

// TestTrySubmitOverload verifies load-shedding: once the pending queue is full,
// TrySubmit returns ErrOverloaded instead of blocking, and drains recover.
func TestTrySubmitOverload(t *testing.T) {
	// No collector goroutine, no gRPC client — we only exercise the queue.
	bs := newBatchScheduler(nil)
	bs.submitCh = make(chan *batchItem, 1)

	ch1, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "1"})
	if err != nil {
		t.Fatalf("first TrySubmit: %v", err)
	}
	if ch1 == nil {
		t.Fatal("first TrySubmit returned nil events channel")
	}

	// Queue is full now → shed the load.
	if _, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "2"}); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("second TrySubmit = %v, want ErrOverloaded", err)
	}

	// Drain one slot → the next submit succeeds again.
	<-bs.submitCh
	ch2, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "3"})
	if err != nil {
		t.Fatalf("third TrySubmit: %v", err)
	}
	if ch2 == nil {
		t.Fatal("third TrySubmit returned nil events channel")
	}
}

// TestSubmitBlocksButTrySubmitSheds ensures Submit still enqueues when there is
// room, and that a full queue makes Submit block (rather than panic/overwrite).
func TestSubmitEnqueues(t *testing.T) {
	bs := newBatchScheduler(nil)
	bs.submitCh = make(chan *batchItem, 1)

	// Blocking Submit fills the slot.
	ch := bs.Submit(context.Background(), GenerateRequest{RequestID: "a"})
	if ch == nil {
		t.Fatal("Submit returned nil events channel")
	}

	// TrySubmit sees the queue as full.
	if _, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "b"}); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("TrySubmit on full queue = %v, want ErrOverloaded", err)
	}
}

// TestSetMaxInFlightClampsAndResizesSlots verifies the Batch Slot count is
// resized and clamped to at least 1.
func TestSetMaxInFlightClampsAndResizesSlots(t *testing.T) {
	bs := newBatchScheduler(nil)

	bs.SetMaxInFlight(2)
	if bs.maxInFlight != 2 || cap(bs.slots) != 2 {
		t.Fatalf("after SetMaxInFlight(2): maxInFlight=%d cap=%d, want 2/2", bs.maxInFlight, cap(bs.slots))
	}

	bs.SetMaxInFlight(0)
	if bs.maxInFlight != 1 || cap(bs.slots) != 1 {
		t.Fatalf("after SetMaxInFlight(0): maxInFlight=%d cap=%d, want 1/1", bs.maxInFlight, cap(bs.slots))
	}
}

// --- fake batch client ---

// fakeStream is a scripted batch RPC stream. Recv returns queued responses,
// then io.EOF when the channel closes; it also honours ctx cancellation so
// Shutdown can unblock a dispatch.
type fakeStream struct {
	ctx context.Context
	ch  chan *pb.BatchGenerateResponse
}

func (s *fakeStream) Recv() (*pb.BatchGenerateResponse, error) {
	select {
	case r, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		return r, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type fakeClient struct {
	calls chan *fakeStream
}

func (c *fakeClient) BatchGenerate(ctx context.Context, _ *pb.BatchGenerateRequest) (batchStream, error) {
	s := &fakeStream{ctx: ctx, ch: make(chan *pb.BatchGenerateResponse, 16)}
	c.calls <- s
	return s, nil
}

func finalEvent(id string) *pb.BatchGenerateResponse {
	return &pb.BatchGenerateResponse{
		RequestId:    id,
		EventType:    pb.GenerateEventType_EVENT_FINAL,
		StopReason:   pb.StopReason_STOP_END_TURN,
		FinishReason: "stop",
	}
}

func tokenEvent(id, tok string) *pb.BatchGenerateResponse {
	return &pb.BatchGenerateResponse{
		RequestId: id,
		EventType: pb.GenerateEventType_EVENT_TOKEN,
		Token:     tok,
	}
}

// startTestScheduler starts the collector with a tiny window for deterministic tests.
func startTestScheduler(fc *fakeClient) *BatchScheduler {
	bs := newBatchScheduler(fc)
	bs.SetBatchWindow(2 * time.Millisecond)
	bs.wg.Add(1)
	go bs.collectorLoop()
	return bs
}

// TestBatchSlotBoundsInFlight verifies that with one Batch Slot the collector
// does not dispatch a second batch until the first releases the slot.
func TestBatchSlotBoundsInFlight(t *testing.T) {
	fc := &fakeClient{calls: make(chan *fakeStream, 4)}
	bs := startTestScheduler(fc)
	defer bs.Shutdown()

	ctx := context.Background()
	ch1, err := bs.TrySubmit(ctx, GenerateRequest{RequestID: "1"})
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	s1 := <-fc.calls // batch 1 dispatched, holds the only slot

	// Submit a second request only after batch 1 is in flight.
	ch2, err := bs.TrySubmit(ctx, GenerateRequest{RequestID: "2"})
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}

	select {
	case <-fc.calls:
		t.Fatal("batch 2 dispatched while batch 1 held the only slot")
	case <-time.After(80 * time.Millisecond):
		// expected: collector blocked on the semaphore
	}

	// Finish batch 1 → its slot frees and batch 2 dispatches.
	s1.ch <- finalEvent("1")
	close(s1.ch)
	s2 := <-fc.calls
	s2.ch <- finalEvent("2")
	close(s2.ch)

	drainEvents(t, ch1)
	drainEvents(t, ch2)
}

// TestEOFWithoutFinalClosesChannel is the regression for the goroutine leak:
// when the worker ends the stream without a final event, every request channel
// must still close.
func TestEOFWithoutFinalClosesChannel(t *testing.T) {
	fc := &fakeClient{calls: make(chan *fakeStream, 4)}
	bs := startTestScheduler(fc)
	defer bs.Shutdown()

	ch, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "x"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	s := <-fc.calls
	s.ch <- tokenEvent("x", "hi")
	close(s.ch) // EOF before any final

	var final string
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.Type == "final" {
				final = ev.StopReason
			}
		case <-timeout:
			t.Fatal("channel did not close after EOF")
		}
	}
	if final != "STOP_ERROR" {
		t.Fatalf("final stop reason = %q, want STOP_ERROR", final)
	}
}

// TestShutdownUnblocksCollector verifies Shutdown returns even while a batch is
// in flight and the in-flight request channel gets a terminal event.
func TestShutdownUnblocksCollector(t *testing.T) {
	fc := &fakeClient{calls: make(chan *fakeStream, 4)}
	bs := startTestScheduler(fc)

	ch, err := bs.TrySubmit(context.Background(), GenerateRequest{RequestID: "s"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-fc.calls // in flight

	done := make(chan struct{})
	go func() { bs.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	drainEvents(t, ch)
}

func drainEvents(t *testing.T, ch <-chan GenerateEvent) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("drain timed out")
		}
	}
}
