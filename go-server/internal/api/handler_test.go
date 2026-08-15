package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/controlplane"
)

// TestHandleUIRouting: handleUI route theo path chính xác, không gated (tạo file tạm).
func TestHandleUIRouting(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "chat.html", "keys.html"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	h := &Handler{uiDir: dir}
	for path, want := range map[string]string{"/": "index.html", "/chat": "chat.html", "/keys": "keys.html"} {
		rec := httptest.NewRecorder()
		h.handleUI(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → code %d body %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	rec := httptest.NewRecorder()
	h.handleUI(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/nope → code %d, want 404", rec.Code)
	}
}

type fakeResolver struct {
	d   *controlplane.Deployment
	err error
}

func (f *fakeResolver) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*controlplane.Deployment, error) {
	return f.d, f.err
}

type fakeLimiter struct {
	allow    bool
	allowErr error
	acquire  bool
}

func (f *fakeLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	return f.allow, f.allowErr
}
func (f *fakeLimiter) Acquire(ctx context.Context, key string, limit int) (bool, error) { return f.acquire, nil }
func (f *fakeLimiter) Release(ctx context.Context, key string) error                     { return nil }

func TestResolveForTenantNotFound(t *testing.T) {
	h := &Handler{resolver: &fakeResolver{err: controlplane.ErrNotFound}, limiter: &fakeLimiter{}, rpmLimit: 60, concLimit: 4}
	rec := httptest.NewRecorder()
	if _, _, ok := h.resolveForTenant(context.Background(), rec, "t1", "qwen-3b"); ok {
		t.Fatal("want ok=false on not found")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestResolveForTenantRateLimited(t *testing.T) {
	h := &Handler{
		resolver: &fakeResolver{d: &controlplane.Deployment{ID: "d1", TenantID: "t1"}},
		limiter:  &fakeLimiter{allow: false, acquire: true},
		rpmLimit: 60, concLimit: 4,
	}
	rec := httptest.NewRecorder()
	if _, _, ok := h.resolveForTenant(context.Background(), rec, "t1", "qwen-3b"); ok {
		t.Fatal("want ok=false on rate limited")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
}

func TestResolveForTenantFailOpen(t *testing.T) {
	h := &Handler{
		resolver: &fakeResolver{d: &controlplane.Deployment{ID: "d1", TenantID: "t1"}},
		limiter:  &fakeLimiter{allow: false, allowErr: errors.New("redis down"), acquire: true},
		rpmLimit: 60, concLimit: 4,
	}
	rec := httptest.NewRecorder()
	d, release, ok := h.resolveForTenant(context.Background(), rec, "t1", "qwen-3b")
	if !ok || d == nil || release == nil {
		t.Fatal("want ok=true on fail-open (redis error)")
	}
}
