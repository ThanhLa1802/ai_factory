package auth

import (
	"context"
	"errors"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidAPIKey      = errors.New("invalid API key")
	ErrKeyInactive        = errors.New("API key inactive")
)

// Store is the persistence dependency of Service; *controlplane.Service implements it.
type Store interface {
	GetUserByUsername(ctx context.Context, username string) (*controlplane.User, string, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*controlplane.APIKey, error)
}

// Service authenticates users (login) and machine clients (API keys).
type Service struct {
	store    Store
	secret   []byte
	tokenTTL time.Duration
}

func NewService(store Store, secret []byte, ttl time.Duration) *Service {
	return &Service{store: store, secret: secret, tokenTTL: ttl}
}

// Login verifies credentials and returns an access token, or ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	user, hash, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	if user.Status != "ACTIVE" {
		return "", ErrInvalidCredentials
	}
	if !VerifyPassword(hash, password) {
		return "", ErrInvalidCredentials
	}
	return IssueToken(s.secret, user.ID, user.TenantID, user.Role, s.tokenTTL)
}

// AuthenticateAPIKey hashes the raw key, looks it up, and checks status/expiry.
func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey string) (*controlplane.APIKey, error) {
	key, err := s.store.GetAPIKeyByHash(ctx, HashAPIKey(rawKey))
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if key.Status != "ACTIVE" {
		return nil, ErrKeyInactive
	}
	if key.ExpiresAt != nil && time.Now().UTC().After(*key.ExpiresAt) {
		return nil, ErrKeyInactive
	}
	return key, nil
}
