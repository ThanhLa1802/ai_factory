package di

import "fmt"

// ProviderError wraps a provider failure and names the provider.
type ProviderError struct {
	Name string
	Err  error
}

func (e *ProviderError) Error() string { return fmt.Sprintf("di: provider %q: %v", e.Name, e.Err) }
func (e *ProviderError) Unwrap() error { return e.Err }

// CircularDependencyError is returned when resolving a provider re-enters itself.
type CircularDependencyError struct{ Name string }

func (e *CircularDependencyError) Error() string {
	return fmt.Sprintf("di: circular dependency at %q", e.Name)
}
