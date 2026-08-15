package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

type fakeStore struct {
	user *controlplane.User
	hash string
	key  *controlplane.APIKey
	err  error
}

func (f *fakeStore) GetUserByUsername(ctx context.Context, username string) (*controlplane.User, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	if f.user == nil {
		return nil, "", errors.New("not found")
	}
	return f.user, f.hash, nil
}

func (f *fakeStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*controlplane.APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.key == nil {
		return nil, errors.New("not found")
	}
	return f.key, nil
}

func TestLoginSuccess(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	store := &fakeStore{user: &controlplane.User{ID: "u1", TenantID: "t1", Role: RoleTenantAdmin, Status: "ACTIVE"}, hash: hash}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
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
	store := &fakeStore{user: &controlplane.User{ID: "u1", TenantID: "t1", Role: RoleTenantAdmin, Status: "INACTIVE"}, hash: hash}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.Login(context.Background(), "admin", "correct-password"); err != ErrInvalidCredentials {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	store := &fakeStore{user: &controlplane.User{ID: "u1", TenantID: "t1", Role: RoleTenantViewer, Status: "ACTIVE"}, hash: hash}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.Login(context.Background(), "admin", "wrong"); err != ErrInvalidCredentials {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateAPIKeyHappy(t *testing.T) {
	store := &fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE"}}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
	key, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever")
	if err != nil {
		t.Fatalf("AuthenticateAPIKey error = %v", err)
	}
	if key.ID != "k1" {
		t.Errorf("key = %+v", key)
	}
}

func TestAuthenticateAPIKeyInactive(t *testing.T) {
	store := &fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t1", Status: "REVOKED"}}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrKeyInactive {
		t.Fatalf("error = %v, want ErrKeyInactive", err)
	}
}

func TestAuthenticateAPIKeyExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	store := &fakeStore{key: &controlplane.APIKey{ID: "k1", TenantID: "t1", Status: "ACTIVE", ExpiresAt: &past}}
	svc := NewService(store, []byte("0123456789abcdef"), time.Hour)
	if _, err := svc.AuthenticateAPIKey(context.Background(), "sk-whatever"); err != ErrKeyInactive {
		t.Fatalf("error = %v, want ErrKeyInactive", err)
	}
}
