// Package di is a minimal, dependency-free lazy DI container with lifecycle
// support and circular-dependency detection. It is intentionally name-based:
// the composition root (internal/app) is the only place that knows strings.
package di

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ProviderFunc builds a component. It may Resolve its own dependencies.
type ProviderFunc func(c *Container) (any, error)

type entry struct {
	name      string
	provider  ProviderFunc
	singleton bool
	instance  any
	resolved  bool
	resolving bool
}

// Container holds registered providers. It is safe for a single composition
// thread (the app boot); it is not designed for concurrent Resolve of the same
// key.
type Container struct {
	mu      sync.Mutex
	entries map[string]*entry
	order   []string // resolution order of singletons (for reverse Shutdown)
}

func NewContainer() *Container {
	return &Container{entries: map[string]*entry{}}
}

// Register adds a transient provider (a fresh instance per Resolve).
func (c *Container) Register(name string, provider ProviderFunc) error {
	return c.register(name, provider, false)
}

// RegisterSingleton adds a lazy singleton provider (built once, then cached).
func (c *Container) RegisterSingleton(name string, provider ProviderFunc) error {
	return c.register(name, provider, true)
}

func (c *Container) register(name string, provider ProviderFunc, singleton bool) error {
	if provider == nil {
		return fmt.Errorf("di: nil provider for %q", name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[name]; ok {
		return fmt.Errorf("di: %q already registered", name)
	}
	c.entries[name] = &entry{name: name, provider: provider, singleton: singleton}
	return nil
}

// Has reports whether a provider is registered under name. It does not build
// the component (useful for role/wiring assertions in tests).
func (c *Container) Has(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[name]
	return ok
}

// Resolve builds (or returns the cached instance of) the named component.
func (c *Container) Resolve(name string) (any, error) {
	c.mu.Lock()
	e, ok := c.entries[name]
	if !ok {
		c.mu.Unlock()
		return nil, &ProviderError{Name: name, Err: errors.New("not registered")}
	}
	if e.resolving {
		c.mu.Unlock()
		return nil, &CircularDependencyError{Name: name}
	}
	if e.singleton && e.resolved {
		inst := e.instance
		c.mu.Unlock()
		return inst, nil
	}
	e.resolving = true
	c.mu.Unlock()

	inst, err := e.provider(c)

	c.mu.Lock()
	e.resolving = false
	if err != nil {
		c.mu.Unlock()
		return nil, &ProviderError{Name: name, Err: err}
	}
	if e.singleton {
		e.instance = inst
		e.resolved = true
		c.order = append(c.order, name)
	}
	c.mu.Unlock()
	return inst, nil
}

// MustResolve is Resolve but panics on error — for use inside providers wired
// by the composition root, where a failure is a programming error.
func (c *Container) MustResolve(name string) any {
	v, err := c.Resolve(name)
	if err != nil {
		panic(err)
	}
	return v
}

// Shutdown calls Shutdown on every resolved singleton implementing Lifecycle,
// in reverse resolution order. Errors are joined.
func (c *Container) Shutdown(ctx context.Context) error {
	return c.closeWith(func(inst any) error {
		if l, ok := inst.(Lifecycle); ok {
			return l.Shutdown(ctx)
		}
		return nil
	})
}

// Close calls Close on every resolved singleton implementing Closer, in reverse
// resolution order.
func (c *Container) Close() error {
	return c.closeWith(func(inst any) error {
		if cl, ok := inst.(Closer); ok {
			return cl.Close()
		}
		return nil
	})
}

func (c *Container) closeWith(fn func(inst any) error) error {
	c.mu.Lock()
	order := append([]string(nil), c.order...)
	insts := make([]any, 0, len(order))
	for _, name := range order {
		if e := c.entries[name]; e != nil {
			insts = append(insts, e.instance)
		}
	}
	c.mu.Unlock()

	var errs []error
	for i := len(insts) - 1; i >= 0; i-- {
		if err := fn(insts[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
