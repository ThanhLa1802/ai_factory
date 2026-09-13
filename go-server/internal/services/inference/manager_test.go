package inference

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingStore is a Store whose LoadSession blocks on a gate, so a test can
// assert that concurrent GetOrCreate calls are coalesced by singleflight.
type countingStore struct {
	mu    sync.Mutex
	loads int
	gate  chan struct{}
}

func (s *countingStore) LoadSession(context.Context, string, string, string) (*Session, error) {
	s.mu.Lock()
	s.loads++
	s.mu.Unlock()
	if s.gate != nil {
		<-s.gate
	}
	return nil, ErrSessionNotFound
}

func (s *countingStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func (*countingStore) UpsertSession(context.Context, *Session) error             { return nil }
func (*countingStore) SessionExists(context.Context, string) (bool, error)       { return false, nil }
func (*countingStore) AppendMessage(context.Context, string, int, Message) error { return nil }
func (*countingStore) ListSessions(context.Context, string, string) ([]SessionSummary, error) {
	return nil, nil
}
func (*countingStore) RenameSession(context.Context, string, string, string, string) error {
	return nil
}
func (*countingStore) DeleteSession(context.Context, string, string, string) error { return nil }

// TestManagerLRUEvicts verifies the in-memory cache is bounded and evicts the
// least-recently-used session.
func TestManagerLRUEvicts(t *testing.T) {
	m := newManager(nil, 2)
	ctx := context.Background()
	if _, err := m.GetOrCreate(ctx, "a", "t", "u"); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := m.GetOrCreate(ctx, "b", "t", "u"); err != nil {
		t.Fatalf("create b: %v", err)
	}
	// Touch a so b becomes the least-recently-used entry.
	if m.Get("a") == nil {
		t.Fatal("a missing")
	}
	if _, err := m.GetOrCreate(ctx, "c", "t", "u"); err != nil {
		t.Fatalf("create c: %v", err)
	}
	if m.Get("b") != nil {
		t.Fatal("b should have been evicted")
	}
	if m.Get("a") == nil || m.Get("c") == nil {
		t.Fatal("a and c should still be cached")
	}
}

// TestManagerCoalescesConcurrentLoads verifies singleflight: N concurrent
// GetOrCreate for the same session trigger a single store load.
func TestManagerCoalescesConcurrentLoads(t *testing.T) {
	st := &countingStore{gate: make(chan struct{})}
	m := newManager(st, 100)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := m.GetOrCreate(ctx, "s", "t", "u"); err != nil {
				t.Errorf("GetOrCreate: %v", err)
			}
		}()
	}
	close(start)
	time.Sleep(50 * time.Millisecond) // let all N reach the singleflight
	close(st.gate)
	wg.Wait()

	if got := st.loadCount(); got != 1 {
		t.Fatalf("LoadSession calls = %d, want 1 (coalesced)", got)
	}
}

// TestManagerRejectsCrossTenantCacheHit verifies the fast path still enforces
// tenant ownership.
func TestManagerRejectsCrossTenantCacheHit(t *testing.T) {
	m := newManager(nil, 10)
	ctx := context.Background()
	if _, err := m.GetOrCreate(ctx, "s", "tenantA", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.GetOrCreate(ctx, "s", "tenantB", ""); err != ErrSessionForbidden {
		t.Fatalf("cross-tenant err = %v, want ErrSessionForbidden", err)
	}
}
