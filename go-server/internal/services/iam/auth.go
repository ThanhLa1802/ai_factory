package iam

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidAPIKey      = errors.New("invalid API key")
	ErrKeyInactive        = errors.New("API key inactive")
)

const (
	apiKeyCacheTTL    = 5 * time.Minute
	apiKeyCachePrefix = "apikey:"
)

// ByteCache is the cache-aside port for API-key lookups. It is implemented by
// infrastructure/cache.KV; a nil cache disables caching.
type ByteCache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

func apiKeyCacheKey(hash string) string { return apiKeyCachePrefix + hash }

// Store is the persistence dependency of AuthService; *Service implements it.
type Store interface {
	GetUserByUsername(ctx context.Context, username string) (*User, string, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error)
}

// AuthService authenticates users (login) and machine clients (API keys).
type AuthService struct {
	store    Store
	secret   []byte
	tokenTTL time.Duration
	cache    ByteCache
}

// NewAuthService builds an AuthService without a cache.
func NewAuthService(store Store, secret []byte, ttl time.Duration) *AuthService {
	return NewAuthServiceWithCache(store, secret, ttl, nil)
}

// NewAuthServiceWithCache builds an AuthService with a cache-aside layer for
// API-key authentication.
func NewAuthServiceWithCache(store Store, secret []byte, ttl time.Duration, cache ByteCache) *AuthService {
	return &AuthService{store: store, secret: secret, tokenTTL: ttl, cache: cache}
}

// Login verifies credentials and returns an access token, or ErrInvalidCredentials.
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
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

// AuthenticateAPIKey hashes the raw key, resolves it via the cache-aside layer
// (falling back to the store on a miss), and checks status/expiry. Inactive or
// expired keys are never cached.
func (s *AuthService) AuthenticateAPIKey(ctx context.Context, rawKey string) (*APIKey, error) {
	hash := HashAPIKey(rawKey)
	if key, ok := s.cachedAPIKey(ctx, hash); ok {
		return s.validateAPIKey(key)
	}
	key, err := s.store.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if _, err := s.validateAPIKey(key); err != nil {
		return nil, err
	}
	s.cacheAPIKey(ctx, hash, key)
	return key, nil
}

// InvalidateAPIKey drops a cached key (call after deleting/revoking it).
func (s *AuthService) InvalidateAPIKey(ctx context.Context, hash string) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.Delete(ctx, apiKeyCacheKey(hash))
}

func (s *AuthService) validateAPIKey(key *APIKey) (*APIKey, error) {
	if key.Status != "ACTIVE" {
		return nil, ErrKeyInactive
	}
	if key.ExpiresAt != nil && time.Now().UTC().After(*key.ExpiresAt) {
		return nil, ErrKeyInactive
	}
	return key, nil
}

// cachedAPIKey reads a key from the cache; any cache error is a miss.
func (s *AuthService) cachedAPIKey(ctx context.Context, hash string) (*APIKey, bool) {
	if s.cache == nil {
		return nil, false
	}
	b, ok, err := s.cache.Get(ctx, apiKeyCacheKey(hash))
	if err != nil || !ok {
		return nil, false
	}
	var key APIKey
	if err := json.Unmarshal(b, &key); err != nil {
		return nil, false
	}
	return &key, true
}

// cacheAPIKey stores a resolved key (best-effort; never fails authentication).
func (s *AuthService) cacheAPIKey(ctx context.Context, hash string, key *APIKey) {
	if s.cache == nil {
		return
	}
	b, err := json.Marshal(key)
	if err != nil {
		return
	}
	_ = s.cache.Set(ctx, apiKeyCacheKey(hash), b, apiKeyCacheTTL)
}
