package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Options{Attempts: 3, InitialDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Options{Attempts: 3, InitialDelay: time.Millisecond}, func() error {
		calls++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("Do = nil, want error")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestDoPermanentStopsImmediately(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Options{Attempts: 5, InitialDelay: time.Millisecond}, func() error {
		calls++
		return Permanent(errors.New("nope"))
	})
	if err == nil || err.Error() != "nope" {
		t.Fatalf("Do = %v, want 'nope'", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (permanent must not retry)", calls)
	}
}

func TestDoAbortsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Do(ctx, Options{Attempts: 10, InitialDelay: time.Second}, func() error {
		return errors.New("boom")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("Do did not abort promptly on ctx cancellation")
	}
}
