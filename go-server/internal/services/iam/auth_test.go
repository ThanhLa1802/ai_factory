package iam

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	user       *User
	hash       string
	key        *APIKey
	err        error
	keyLookups int
}

func (f *fakeStore) GetUserByUsername(ctx context.Context, username string) (*User, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	if f.user == nil {
		return nil, "", errors.New("not found")
	}
	return f.user, f.hash, nil
}

func (f *fakeStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	f.keyLookups++
	if f.err != nil {
		return nil, f.err
	}
	if f.key == nil {
		return nil, errors.New("not found")
	}
	return f.key, nil
}

// fakeCache is an in-memory ByteCache that also counts operations and can be
// told to fail (to prove cache errors fall back to the store).
type fakeCache struct {
	m          map[string][]byte
	gets       int
	sets       int
	dels       int
	errOnGet   bool
	errOnWrite bool
}

func newFakeCache() *fakeCache { return &fakeCache{m: map[string][]byte{}} }

func (c *fakeCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.gets++
	if c.errOnGet {
		return nil, false, errors.New("redis down")
	}
	b, ok := c.m[key]
	return b, ok, nil
}

func (c *fakeCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	c.sets++
	if c.errOnWrite {
		return errors.New("redis down")
	}
	c.m[key] = val
	return nil
}

func (c *fakeCache) Delete(_ context.Context, key string) error {
	c.dels++
	delete(c.m, key)
	return nil
}

func TestLoginSuccess(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	store := &fakeStore{user: &User{ID: "u1", TenantID: "t1", Role: RoleTenantAdmin, Status: "ACTIVE"}, hash: hash}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	token, err := svc.Login(context.Background(), "admin", "correct-password")
	if err != nil {
		t.Fatalf("Login error = %v", err)
	}
	claims, err := ParseToken([]byte("0123456789abcdef"), token)
	if err != nil {
		t.Fatalf("ParseToken error = %v", err)
	}
	if claims.TenantID != "t1" || claims.Role != RoleTenantAdmin {
		t.Errorf("claims = %+v", claims)
	}
}

func TestLoginInactiveUser(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	store := &fakeStore{user: &User{ID: "u1", TenantID: "t1", Role: RoleTenantAdmin, Status: "INACTIVE"}, hash: hash}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.Login(context.Background(), "admin", "correct-password"); err != ErrInvalidCredentials {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	store := &fakeStore{user: &User{ID: "u1", TenantID: "t1", Role: RoleTenantViewer, Status: "ACTIVE"}, hash: hash}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.Login(context.Background(), "admin", "wrong"); err != ErrInvalidCredentials {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateAPIKeyHappy(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE"}}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	key, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever")
	if err != nil {
		t.Fatalf("AuthenticateAPIKey error = %v", err)
	}
	if key.ID != "k1" {
		t.Errorf("key = %+v", key)
	}
}

func TestAuthenticateAPIKeyInactive(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "REVOKED"}}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrKeyInactive {
		t.Fatalf("error = %v, want ErrKeyInactive", err)
	}
}

func TestAuthenticateAPIKeyExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE", ExpiresAt: &past}}
	svc := NewAuthService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrKeyInactive {
		t.Fatalf("error = %v, want ErrKeyInactive", err)
	}
}

func TestAuthenticateAPIKeyCacheHit(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE"}}
	cache := newFakeCache()
	svc := NewAuthServiceWithCache(store, []byte("0123456789abcdef"), time.Hour, cache)

	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != nil {
		t.Fatalf("first auth: %v", err)
	}
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != nil {
		t.Fatalf("second auth: %v", err)
	}
	if store.keyLookups != 1 {
		t.Fatalf("store lookups = %d, want 1 (second served from cache)", store.keyLookups)
	}
	if cache.sets != 1 {
		t.Fatalf("cache sets = %d, want 1", cache.sets)
	}
}

func TestAuthenticateAPIKeyInvalidation(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE"}}
	cache := newFakeCache()
	svc := NewAuthServiceWithCache(store, []byte("0123456789abcdef"), time.Hour, cache)

	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := svc.InvalidateAPIKey(context.Background(), HashAPIKey("sk-whatever")); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	// Remove the key from the store: a second auth can only fail if the cache
	// entry is really gone.
	store.key = nil
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrInvalidAPIKey {
		t.Fatalf("after invalidate+delete error = %v, want ErrInvalidAPIKey", err)
	}
	if cache.dels != 1 {
		t.Fatalf("cache deletes = %d, want 1", cache.dels)
	}
}

func TestAuthenticateAPIKeyInactiveNotCached(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "REVOKED"}}
	cache := newFakeCache()
	svc := NewAuthServiceWithCache(store, []byte("0123456789abcdef"), time.Hour, cache)

	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrKeyInactive {
		t.Fatalf("error = %v, want ErrKeyInactive", err)
	}
	if cache.sets != 0 {
		t.Fatalf("cache sets = %d, want 0 (inactive keys must not be cached)", cache.sets)
	}
}

func TestAuthenticateAPIKeyCacheErrorFallsBack(t *testing.T) {
	store := &fakeStore{key: &APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE"}}
	cache := newFakeCache()
	cache.errOnGet = true
	svc := NewAuthServiceWithCache(store, []byte("0123456789abcdef"), time.Hour, cache)

	key, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever")
	if err != nil || key.ID != "k1" {
		t.Fatalf("auth with cache error = (%+v,%v), want success from store", key, err)
	}
}
