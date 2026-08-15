package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fail() error { return errors.New("boom") }

func TestCallClosesOnSuccess(t *testing.T) {
	cb := New(3, time.Second)
	calls := 0
	for i := 0; i < 5; i++ {
		if err := cb.Call(context.Background(), func() error { calls++; return nil }); err != nil {
			t.Fatalf("call %d = %v, want nil", i, err)
		}
	}
	if calls != 5 {
		t.Fatalf("calls = %d, want 5", calls)
	}
	if cb.State() != StateClosed {
		t.Fatalf("state = %v, want closed", cb.State())
	}
}

func TestCallOpensAfterThresholdAndFailsFast(t *testing.T) {
	cb := New(3, time.Second)
	for i := 0; i < 3; i++ {
		if err := cb.Call(context.Background(), fail); err == nil {
			t.Fatalf("call %d = nil, want error", i)
		}
	}
	if cb.State() != StateOpen {
		t.Fatalf("state = %v, want open", cb.State())
	}
	// While open, the wrapped function must not be invoked.
	invoked := false
	if err := cb.Call(context.Background(), func() error { invoked = true; return nil }); !errors.Is(err, ErrOpen) {
		t.Fatalf("call while open = %v, want ErrOpen", err)
	}
	if invoked {
		t.Fatal("fn invoked while breaker open; want fail-fast")
	}
}

func TestCallProbeSuccessRecovers(t *testing.T) {
	cb := New(1, time.Second)
	fake := time.Now()
	cb.now = func() time.Time { return fake }

	_ = cb.Call(context.Background(), fail) // threshold 1 → open
	if cb.State() != StateOpen {
		t.Fatalf("state = %v, want open", cb.State())
	}
	// Before cooldown: still open.
	fake = fake.Add(500 * time.Millisecond)
	if err := cb.Call(context.Background(), fail); !errors.Is(err, ErrOpen) {
		t.Fatalf("call before cooldown = %v, want ErrOpen", err)
	}
	// After cooldown: probe succeeds → closed.
	fake = fake.Add(1 * time.Second)
	if err := cb.Call(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("probe = %v, want nil", err)
	}
	if cb.State() != StateClosed {
		t.Fatalf("state = %v, want closed after successful probe", cb.State())
	}
}

func TestCallProbeFailureReopens(t *testing.T) {
	cb := New(5, time.Second)
	fake := time.Now()
	cb.now = func() time.Time { return fake }

	// Two failures keep it closed.
	for i := 0; i < 2; i++ {
		_ = cb.Call(context.Background(), fail)
	}
	if cb.State() != StateClosed {
		t.Fatalf("state = %v, want closed before threshold", cb.State())
	}
	// Drive to open.
	for i := 0; i < 3; i++ {
		_ = cb.Call(context.Background(), fail)
	}
	if cb.State() != StateOpen {
		t.Fatalf("state = %v, want open", cb.State())
	}
	// After cooldown the probe fails → reopens immediately.
	fake = fake.Add(2 * time.Second)
	if err := cb.Call(context.Background(), fail); err == nil {
		t.Fatal("probe = nil, want error")
	}
	if cb.State() != StateOpen {
		t.Fatalf("state = %v, want open after failed probe", cb.State())
	}
}
