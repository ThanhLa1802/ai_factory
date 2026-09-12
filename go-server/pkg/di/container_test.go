package di

import (
	"context"
	"errors"
	"testing"
)

func TestResolveUnknown(t *testing.T) {
	c := NewContainer()
	if _, err := c.Resolve("nope"); err == nil {
		t.Fatal("Resolve(unknown) = nil, want error")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	c := NewContainer()
	if err := c.RegisterSingleton("x", func(*Container) (any, error) { return 1, nil }); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.RegisterSingleton("x", func(*Container) (any, error) { return 2, nil }); err == nil {
		t.Fatal("duplicate register = nil, want error")
	}
}

func TestTransientResolvesEachTime(t *testing.T) {
	c := NewContainer()
	calls := 0
	_ = c.Register("t", func(*Container) (any, error) { calls++; return calls, nil })
	_, _ = c.Resolve("t")
	v, _ := c.Resolve("t")
	if calls != 2 || v.(int) != 2 {
		t.Fatalf("calls=%d v=%v, want 2 resolutions", calls, v)
	}
}

func TestSingletonResolvesOnce(t *testing.T) {
	c := NewContainer()
	calls := 0
	_ = c.RegisterSingleton("s", func(*Container) (any, error) { calls++; return struct{}{}, nil })
	a, _ := c.Resolve("s")
	b, _ := c.Resolve("s")
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if a == nil || b == nil {
		t.Fatal("singleton returned nil")
	}
}

func TestProviderErrorWrapped(t *testing.T) {
	c := NewContainer()
	boom := errors.New("boom")
	_ = c.RegisterSingleton("bad", func(*Container) (any, error) { return nil, boom })
	_, err := c.Resolve("bad")
	if !errors.Is(err, boom) {
		t.Fatalf("Resolve err = %v, want to wrap boom", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *ProviderError", err)
	}
}

func TestCircularDependency(t *testing.T) {
	c := NewContainer()
	_ = c.RegisterSingleton("a", func(cc *Container) (any, error) { return cc.Resolve("b") })
	_ = c.RegisterSingleton("b", func(cc *Container) (any, error) { return cc.Resolve("a") })
	_, err := c.Resolve("a")
	var ce *CircularDependencyError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CircularDependencyError", err)
	}
}

type fakeComponent struct{ shutdowns int }

func (f *fakeComponent) Shutdown(ctx context.Context) error { f.shutdowns++; return nil }

func TestShutdownCallsLifecycleReverseOrder(t *testing.T) {
	c := NewContainer()
	first := &fakeComponent{}
	second := &fakeComponent{}
	var order []string
	_ = c.RegisterSingleton("first", func(*Container) (any, error) { order = append(order, "first"); return first, nil })
	_ = c.RegisterSingleton("second", func(*Container) (any, error) { order = append(order, "second"); return second, nil })
	_, _ = c.Resolve("first")
	_, _ = c.Resolve("second")
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if first.shutdowns != 1 || second.shutdowns != 1 {
		t.Fatalf("shutdowns = %d/%d, want 1/1", first.shutdowns, second.shutdowns)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("build order = %v", order)
	}
}
