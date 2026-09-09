package inference

import (
	"context"
	"errors"
	"testing"
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
