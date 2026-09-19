package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/gin-gonic/gin"
)

// newTestEngine returns a Gin engine in test mode (no debug output).
func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// testContext returns a Gin context writing to a fresh recorder.
func testContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	return c, rec
}

// TestHandleUIRouting: handleUI route theo path chính xác, không gated (tạo file tạm).
func TestHandleUIRouting(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "chat.html", "keys.html"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	h := &Handler{uiDir: dir}
	e := newTestEngine()
	e.GET("/", h.handleUI)
	e.GET("/chat", h.handleUI)
	e.GET("/keys", h.handleUI)

	for path, want := range map[string]string{"/": "index.html", "/chat": "chat.html", "/keys": "keys.html"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → code %d body %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/nope → code %d, want 404", rec.Code)
	}
}

type fakeResolver struct {
	d   *ResolvedDeployment
	err error
}

func (f *fakeResolver) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*ResolvedDeployment, error) {
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
func (f *fakeLimiter) Acquire(ctx context.Context, key string, limit int) (bool, error) {
	return f.acquire, nil
}
func (f *fakeLimiter) Release(ctx context.Context, key string) error { return nil }

func TestResolveForTenantNotFound(t *testing.T) {
	h := &Handler{resolver: &fakeResolver{err: errors.New("not found")}, limiter: &fakeLimiter{}, rpmLimit: 60, concLimit: 4}
	c, rec := testContext()
	if _, _, ok := h.resolveForTenant(context.Background(), c, "t1", "qwen-3b"); ok {
		t.Fatal("want ok=false on not found")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestResolveForTenantRateLimited(t *testing.T) {
	h := &Handler{
		resolver: &fakeResolver{d: &ResolvedDeployment{ID: "d1", TenantID: "t1"}},
		limiter:  &fakeLimiter{allow: false, acquire: true},
		rpmLimit: 60, concLimit: 4,
	}
	c, rec := testContext()
	if _, _, ok := h.resolveForTenant(context.Background(), c, "t1", "qwen-3b"); ok {
		t.Fatal("want ok=false on rate limited")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
}

func TestResolveForTenantFailOpen(t *testing.T) {
	h := &Handler{
		resolver: &fakeResolver{d: &ResolvedDeployment{ID: "d1", TenantID: "t1"}},
		limiter:  &fakeLimiter{allow: false, allowErr: errors.New("redis down"), acquire: true},
		rpmLimit: 60, concLimit: 4,
	}
	c, _ := testContext()
	d, release, ok := h.resolveForTenant(context.Background(), c, "t1", "qwen-3b")
	if !ok || d == nil || release == nil {
		t.Fatal("want ok=true on fail-open (redis error)")
	}
}

type fakeGate struct {
	reserveID  string
	reserveErr error
	settles    int
	releases   int
}

func (f *fakeGate) Reserve(ctx context.Context, tenantID, model string, estInput, maxOutput int) (string, error) {
	return f.reserveID, f.reserveErr
}
func (f *fakeGate) Settle(ctx context.Context, reservationID string, prompt, completion int) error {
	f.settles++
	return nil
}
func (f *fakeGate) Release(ctx context.Context, reservationID string) error {
	f.releases++
	return nil
}

func TestReserveBillingInsufficient(t *testing.T) {
	h := &Handler{billing: &fakeGate{reserveErr: ErrInsufficientCredits}}
	c, rec := testContext()
	if _, ok := h.reserveBilling(context.Background(), c, "t1", "qwen-3b", nil, infra.DefaultSamplingParams()); ok {
		t.Fatal("want ok=false on insufficient credits")
	}
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("code = %d, want 402", rec.Code)
	}
}

func TestReserveBillingDisabled(t *testing.T) {
	h := &Handler{} // nil gate disables billing
	id, ok := h.reserveBilling(context.Background(), nil, "t1", "qwen-3b", nil, infra.DefaultSamplingParams())
	if !ok || id != "" {
		t.Fatalf("disabled gate = (%q,%v), want (\"\",true)", id, ok)
	}
}

type fakeQuotaGate struct{ err error }

func (f *fakeQuotaGate) Check(ctx context.Context, tenantID string) error { return f.err }

func TestCheckQuotaExceeded(t *testing.T) {
	h := &Handler{quota: &fakeQuotaGate{err: ErrQuotaExceeded}}
	c, rec := testContext()
	if h.checkQuota(context.Background(), c, "t1") {
		t.Fatal("want ok=false on quota exceeded")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
}

func TestCheckQuotaDisabled(t *testing.T) {
	h := &Handler{} // nil gate disables quotas
	if !h.checkQuota(context.Background(), nil, "t1") {
		t.Fatal("nil gate must allow")
	}
}

func TestCheckQuotaFailOpen(t *testing.T) {
	h := &Handler{quota: &fakeQuotaGate{err: errors.New("db down")}}
	c, _ := testContext()
	if !h.checkQuota(context.Background(), c, "t1") {
		t.Fatal("want fail-open on infra error")
	}
}

// TestLastUserTurn: only the final user message is the turn to answer; the
// messages before it are history. A request without a user message is rejected.
func TestLastUserTurn(t *testing.T) {
	msg := func(role, content string) Message { return Message{Role: role, Content: content} }

	t.Run("no user message", func(t *testing.T) {
		_, _, ok := lastUserTurn([]Message{msg(RoleAssistant, "hi")})
		if ok {
			t.Fatal("want ok=false")
		}
	})

	t.Run("single user message", func(t *testing.T) {
		history, turn, ok := lastUserTurn([]Message{msg(RoleUser, "hello")})
		if !ok || len(history) != 0 || turn.Content != "hello" {
			t.Fatalf("got history=%v turn=%q ok=%v", history, turn.Content, ok)
		}
	})

	t.Run("history then user", func(t *testing.T) {
		in := []Message{
			msg(RoleUser, "hi"),
			msg(RoleAssistant, "hello"),
			msg(RoleUser, "area of a triangle?"),
		}
		history, turn, ok := lastUserTurn(in)
		if !ok || len(history) != 2 || turn.Content != "area of a triangle?" {
			t.Fatalf("got history=%d turn=%q ok=%v", len(history), turn.Content, ok)
		}
		if history[0].Role != RoleUser || history[1].Role != RoleAssistant {
			t.Fatalf("history roles = %q,%q", history[0].Role, history[1].Role)
		}
	})

	t.Run("trailing assistant is ignored", func(t *testing.T) {
		in := []Message{
			msg(RoleUser, "hi"),
			msg(RoleAssistant, "hello"),
		}
		history, turn, ok := lastUserTurn(in)
		if !ok || len(history) != 0 || turn.Content != "hi" {
			t.Fatalf("got history=%d turn=%q ok=%v", len(history), turn.Content, ok)
		}
	})
}

func TestSettleReleaseBilling(t *testing.T) {
	g := &fakeGate{}
	h := &Handler{billing: g}
	h.settleBilling("r1", 10, 5)
	h.releaseBilling("r2")
	if g.settles != 1 || g.releases != 1 {
		t.Fatalf("settles=%d releases=%d, want 1/1", g.settles, g.releases)
	}
	// Empty reservation id (billing off) is a no-op.
	h2 := &Handler{billing: g}
	h2.settleBilling("", 1, 1)
	h2.releaseBilling("")
	if g.settles != 1 || g.releases != 1 {
		t.Fatalf("empty id changed counters: settles=%d releases=%d", g.settles, g.releases)
	}
}
