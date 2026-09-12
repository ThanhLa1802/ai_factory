package di

import "context"

// Lifecycle is implemented by components that need cleanup. Shutdown is called
// once per resolved singleton, in reverse resolution order.
type Lifecycle interface {
	Shutdown(ctx context.Context) error
}

// Closer is a lighter lifecycle for components exposing Close() error.
type Closer interface {
	Close() error
}
