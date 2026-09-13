package usage

import (
	"errors"

	"gorm.io/gorm"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// Service is the control plane use-case layer. It depends only on repository
// interfaces — it never sees *gorm.DB.
type Service struct {
	repos   Repositories
	counter Counter
}

// NewService builds the service over a repository bundle (direct DB writes).
func NewService(repos Repositories) *Service { return &Service{repos: repos} }

// NewServiceWithCounter builds the service with a Redis counter: usage is
// buffered and flushed to the aggregate table, and reads come from there.
func NewServiceWithCounter(repos Repositories, counter Counter) *Service {
	return &Service{repos: repos, counter: counter}
}

// NewServiceFromGorm is a convenience constructor for tests/wiring that builds
// the GORM-backed repository bundle first.
func NewServiceFromGorm(db *gorm.DB) *Service { return NewService(NewRepositories(db)) }
