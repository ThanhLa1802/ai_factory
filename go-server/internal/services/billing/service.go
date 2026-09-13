package billing

import (
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// Enforcement modes (design D9).
const (
	ModeOff     = "off"     // billing disabled: no reserve/settle
	ModeShadow  = "shadow"  // reserve/settle run but never block
	ModeEnforce = "enforce" // insufficient credits are rejected (402)
)

// Sentinel errors.
var (
	ErrNotFound            = errors.New("not found")
	ErrInsufficientCredits = errors.New("insufficient credits")
	ErrNoPrice             = errors.New("no price for model")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	ErrInvalidAmount       = errors.New("invalid amount")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrInvalidPrice        = errors.New("invalid price")
)

// Config configures the billing service.
type Config struct {
	Mode             string
	Currency         string
	InitialAllowance int64
	ReservationTTL   time.Duration
	Log              *slog.Logger
}

// Service is the billing use-case layer; it never sees *gorm.DB. Money is
// integer micro-credits throughout.
type Service struct {
	repos    Repositories
	cfg      Config
	provider PaymentProvider
}

// NewService builds the service over a repository bundle.
func NewService(repos Repositories, cfg Config, provider PaymentProvider) *Service {
	if cfg.Currency == "" {
		cfg.Currency = "USD"
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeShadow
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 15 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if provider == nil {
		provider = MockProvider{}
	}
	return &Service{repos: repos, cfg: cfg, provider: provider}
}

// NewServiceFromGorm is a convenience constructor for tests/wiring.
func NewServiceFromGorm(db *gorm.DB, cfg Config) *Service {
	return NewService(NewRepositories(db), cfg, MockProvider{})
}
