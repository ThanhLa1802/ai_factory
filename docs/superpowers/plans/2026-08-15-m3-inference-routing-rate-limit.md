# M3 Inference Routing + Rate Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Nối inference gateway `/v1/chat/completions` vào control plane — resolve request `model` thành deployment READY của tenant (tenant isolation ở data plane) + rate limit (tenant RPM + deployment concurrency, Redis).

**Architecture:** Thêm `controlplane.ResolveDeployment` (SQL join models→model_versions→deployments) + package `ratelimit` (interface `Limiter` + `RedisLimiter` qua go-redis/v9). Handler inject `DeploymentResolver` + `Limiter`, resolve + rate-limit trước loop, fail-open khi Redis down. Seed demo model `qwen-3b` + deployment READY.

**Tech Stack:** Go 1.25, pgx v5, go-redis/v9, miniredis v2 (test-only), stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-08-15-inference-routing-rate-limit-design.md`

## Global Constraints

- Go 1.25.7 (`go.mod`).
- **Không sửa proto**, không đụng Python worker — contract inference giữ nguyên.
- Error shape: inference route dùng `writeOpenAIError` (đã có, field `type`/`message`/`code`); control plane dùng `writeAPIError` (field `code`/`message`). Spec §6 nói `{"error":{"code":...}}` nhưng helper inference hiện có dùng `type` — giữ nguyên helper hiện có (surgical).
- Test Postgres-gated: skip khi `AI_FACTORY_DATABASE_URL` trống (theo mẫu `deployment_test.go`).
- Không log token / API key / prompt.
- `controlplane.ErrNotFound` đã tồn tại (`users.go`) — dùng lại, không tạo mới.
- Hằng state dùng `controlplane.DeploymentReady` v.v. (đã có `state.go`), không literal `'READY'`.
- Tên model seed demo: `"qwen-3b"`.

---

## File Structure

| File | Trách nhiệm |
|---|---|
| `internal/controlplane/deployment.go` | Thêm `ResolveDeployment` |
| `internal/controlplane/deployment_test.go` | Integration test resolve |
| `internal/auth/middleware.go` | Thêm `TenantIDFromContext` |
| `internal/auth/middleware_test.go` | Unit test helper |
| `internal/ratelimit/limiter.go` | Interface `Limiter` |
| `internal/ratelimit/redis.go` | `RedisLimiter` (go-redis/v9) |
| `internal/ratelimit/redis_test.go` | Unit test với miniredis |
| `internal/config/config.go` + `config_test.go` | Thêm `RedisAddr`, `RateLimitRPM`, `RateLimitConcurrency` |
| `internal/api/handler.go` | Inject resolver+limiter; `resolveForTenant`; gọi trong `handleOpenAIChatCompletions` |
| `internal/api/handler_test.go` | Unit test resolve/rate-limit/fail-open (fake resolver+limiter) |
| `internal/observability/metrics.go` | Thêm interface `RouteLabelSetter` (bonus D7) |
| `cmd/server/main.go` | Wire redis client + limiter + seed demo; `statusRecorder` lưu labels + metrics middleware điền |
| `cmd/server/seed.go` | Thêm `seedDemo` (model + version + deployment READY) |
| `deployments/docker-compose.yml` | Thêm service `redis` |
| `go.mod` / `go.sum` | Thêm `go-redis/v9` + `miniredis/v2` |
| `CLAUDE.md`, `docs/TRACKING.md` | Cập nhật docs |

---

## Task 1: `controlplane.ResolveDeployment`

**Files:**
- Modify: `internal/controlplane/deployment.go` (thêm method + import `pgx`)
- Test: `internal/controlplane/deployment_test.go` (thêm test mới)

**Interfaces:**
- Consumes: `Service.db *pgxpool.Pool`, `Deployment` struct (đã có), `DeploymentReady` hằng (đã có), `ErrNotFound` (đã có).
- Produces: `func (s *Service) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*Deployment, error)` — trả deployment READY mới nhất hoặc `ErrNotFound`.

- [ ] **Step 1: Viết test fail**

Thêm vào `deployment_test.go` (integration, gated `AI_FACTORY_DATABASE_URL`, theo đúng mẫu `TestWorkloadRefAndEndpointIntegration`). Thêm import `"errors"` vào block import.

```go
// TestResolveDeploymentIntegration resolves model name → READY deployment with
// tenant scoping, against live Postgres.
func TestResolveDeploymentIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(d.Pool())

	tenant, err := s.CreateTenant(ctx, "rd-tenant-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	model, err := s.CreateModel(ctx, Model{Name: "rd-model-" + uuid.NewString()[:8], Task: "text-generation", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	mv, err := s.CreateModelVersion(ctx, ModelVersion{ModelID: model.ID, Version: "v1", ArtifactURI: "local://m"})
	if err != nil {
		t.Fatalf("create model version: %v", err)
	}
	tpl, err := s.CreateTemplate(ctx, ServingTemplate{Name: "rd-template-" + uuid.NewString()[:8], Runtime: "transformers"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	tv, err := s.CreateTemplateVersion(ctx, TemplateVersion{TemplateID: tpl.ID, Version: "v1", Image: "img:latest"})
	if err != nil {
		t.Fatalf("create template version: %v", err)
	}
	deploy, err := s.CreateDeployment(ctx, Deployment{
		TenantID: tenant.ID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "svc", Region: "us-east-1", DesiredReplicas: 1,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// PENDING deployment must NOT resolve.
	if _, err := s.ResolveDeployment(ctx, tenant.ID, model.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve PENDING = %v, want ErrNotFound", err)
	}

	// Drive to READY.
	for _, to := range []string{DeploymentProvisioning, DeploymentStarting, DeploymentReady} {
		if _, err := s.TransitionDeployment(ctx, deploy.ID, to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}

	got, err := s.ResolveDeployment(ctx, tenant.ID, model.Name)
	if err != nil {
		t.Fatalf("resolve READY: %v", err)
	}
	if got.ID != deploy.ID {
		t.Fatalf("resolved %q, want %q", got.ID, deploy.ID)
	}

	// Wrong tenant → ErrNotFound (tenant isolation).
	if _, err := s.ResolveDeployment(ctx, "some-other-tenant", model.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve wrong tenant = %v, want ErrNotFound", err)
	}

	// Unknown model name → ErrNotFound.
	if _, err := s.ResolveDeployment(ctx, tenant.ID, "no-such-model"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve unknown model = %v, want ErrNotFound", err)
	}

	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM models WHERE id = $1`, model.ID)
		_, _ = d.Pool().Exec(ctx, `DELETE FROM serving_templates WHERE id = $1`, tpl.ID)
	})
}
```

- [ ] **Step 2: Run test, verify fail**

Run: `cd go-server && go test ./internal/controlplane/ -run TestResolveDeploymentIntegration -v`
Expected: FAIL — `s.ResolveDeployment undefined`.

- [ ] **Step 3: Implement `ResolveDeployment`**

Trong `deployment.go`, thêm import `"github.com/jackc/pgx/v5"` vào block import (đang có `errors`). Thêm method sau `ListDeployments`:

```go
// ResolveDeployment trả deployment READY mới nhất của tenant serve model `modelName`.
// ErrNotFound nếu không có model/version/deployment READY khớp tenant.
func (s *Service) ResolveDeployment(ctx context.Context, tenantID, modelName string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx,
		`SELECT d.id, d.tenant_id, d.model_version_id, d.template_version_id, d.name,
		        d.region, d.desired_replicas, d.status, d.workload_ref, d.created_at, d.updated_at
		 FROM deployments d
		 JOIN model_versions mv ON mv.id = d.model_version_id
		 JOIN models m        ON m.id  = mv.model_id
		 WHERE m.name = $1 AND d.tenant_id = $2 AND d.status = $3
		 ORDER BY d.created_at DESC
		 LIMIT 1`,
		modelName, tenantID, DeploymentReady).
		Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.WorkloadRef, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve deployment: %w", err)
	}
	return &d, nil
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `cd go-server && go test ./internal/controlplane/ -run TestResolveDeploymentIntegration -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/controlplane/deployment.go internal/controlplane/deployment_test.go
git commit -m "feat(controlplane): ResolveDeployment — model name → READY deployment (tenant-scoped)"
```

---

## Task 2: `auth.TenantIDFromContext`

**Files:**
- Modify: `internal/auth/middleware.go` (thêm helper)
- Test: `internal/auth/middleware_test.go`

**Interfaces:**
- Consumes: `ClaimsFromContext` (đã có), `APIKeyFromContext` (đã có), `Claims.TenantID`, `controlplane.APIKey.TenantID`.
- Produces: `func TenantIDFromContext(ctx context.Context) (string, bool)`.

- [ ] **Step 1: Viết test fail**

Thêm vào `middleware_test.go` (test ở package `auth`, truy cập được `ctxKey{}`/`apiKeyCtxKey{}`):

```go
func TestTenantIDFromContext(t *testing.T) {
	ctx := context.Background()

	if _, ok := TenantIDFromContext(ctx); ok {
		t.Fatal("empty context → want ok=false")
	}

	claims := &Claims{UserID: "u1", TenantID: "tenant-1", Role: RoleTenantAdmin}
	if id, ok := TenantIDFromContext(context.WithValue(ctx, ctxKey{}, claims)); !ok || id != "tenant-1" {
		t.Fatalf("claims → (%q,%v), want (tenant-1,true)", id, ok)
	}

	key := &controlplane.APIKey{ID: "k1", TenantID: "tenant-2"}
	if id, ok := TenantIDFromContext(context.WithValue(ctx, apiKeyCtxKey{}, key)); !ok || id != "tenant-2" {
		t.Fatalf("api key → (%q,%v), want (tenant-2,true)", id, ok)
	}
}
```

Thêm import `"context"` vào block import của `middleware_test.go` (nếu chưa có).

- [ ] **Step 2: Run test, verify fail**

Run: `cd go-server && go test ./internal/auth/ -run TestTenantIDFromContext -v`
Expected: FAIL — `TenantIDFromContext undefined`.

- [ ] **Step 3: Implement**

Thêm vào `middleware.go`, sau `APIKeyFromContext`:

```go
// TenantIDFromContext trả tenant từ Claims (JWT) hoặc APIKey, bất kể đường auth nào.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	if c, ok := ClaimsFromContext(ctx); ok {
		return c.TenantID, true
	}
	if k, ok := APIKeyFromContext(ctx); ok {
		return k.TenantID, true
	}
	return "", false
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `cd go-server && go test ./internal/auth/ -run TestTenantIDFromContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/auth/middleware.go internal/auth/middleware_test.go
git commit -m "feat(auth): TenantIDFromContext — tenant từ JWT hoặc API key"
```

---

## Task 3: package `ratelimit` (interface + Redis impl)

**Files:**
- Create: `internal/ratelimit/limiter.go`
- Create: `internal/ratelimit/redis.go`
- Test: `internal/ratelimit/redis_test.go`

**Interfaces:**
- Produces: `type Limiter interface { Allow(ctx, key string, limit int, window time.Duration) (bool, error); Acquire(ctx, key string, limit int) (bool, error); Release(ctx, key string) error }`; `func NewRedisLimiter(rdb *redis.Client) *RedisLimiter`.

- [ ] **Step 1: Thêm dependencies**

```bash
cd /g/STUDY/AI/ai_factory/go-server
go get github.com/redis/go-redis/v9
go get github.com/alicebob/miniredis/v2
```

- [ ] **Step 2: Viết test fail**

Tạo `redis_test.go`:

```go
package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLimiter(t *testing.T) *RedisLimiter {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewRedisLimiter(rdb)
}

func TestAllowFixedWindow(t *testing.T) {
	ctx := context.Background()
	l := newTestLimiter(t)

	for i := 0; i < 3; i++ {
		ok, err := l.Allow(ctx, "k", 3, time.Minute)
		if err != nil || !ok {
			t.Fatalf("allow %d = (%v,%v), want true", i, ok, err)
		}
	}
	ok, err := l.Allow(ctx, "k", 3, time.Minute)
	if err != nil || ok {
		t.Fatalf("allow over limit = (%v,%v), want false", ok, err)
	}
}

func TestAcquireRelease(t *testing.T) {
	ctx := context.Background()
	l := newTestLimiter(t)

	for i := 0; i < 2; i++ {
		ok, err := l.Acquire(ctx, "c", 2)
		if err != nil || !ok {
			t.Fatalf("acquire %d = (%v,%v), want true", i, ok, err)
		}
	}
	if ok, _ := l.Acquire(ctx, "c", 2); ok {
		t.Fatal("acquire over limit = true, want false")
	}
	if err := l.Release(ctx, "c"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ok, _ := l.Acquire(ctx, "c", 2); !ok {
		t.Fatal("acquire after release = false, want true")
	}
}

func TestAllowErrorOnUnreachableRedis(t *testing.T) {
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { rdb.Close() })
	l := NewRedisLimiter(rdb)
	if _, err := l.Allow(ctx, "k", 1, time.Minute); err == nil {
		t.Fatal("allow on unreachable redis = nil error, want error")
	}
}
```

- [ ] **Step 3: Run test, verify fail**

Run: `cd go-server && go test ./internal/ratelimit/ -v`
Expected: FAIL — package `ratelimit` chưa tồn tại / `NewRedisLimiter` undefined.

- [ ] **Step 4: Implement interface + Redis**

Tạo `limiter.go`:

```go
// Package ratelimit cung cấp rate limiting cho inference gateway.
package ratelimit

import (
	"context"
	"time"
)

// Limiter kiểm tra fixed-window counter (Allow) và concurrency (Acquire/Release).
// Lỗi trả về (vd Redis down) để caller quyết fail-open.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
	Acquire(ctx context.Context, key string, limit int) (bool, error)
	Release(ctx context.Context, key string) error
}
```

Tạo `redis.go`:

```go
package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLimiter dùng Redis counter: fixed-window cho Allow, INCR/DECR cho concurrency.
type RedisLimiter struct {
	rdb *redis.Client
}

func NewRedisLimiter(rdb *redis.Client) *RedisLimiter { return &RedisLimiter{rdb: rdb} }

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		if err := l.rdb.Expire(ctx, key, window).Err(); err != nil {
			return false, err
		}
	}
	return n <= int64(limit), nil
}

func (l *RedisLimiter) Acquire(ctx context.Context, key string, limit int) (bool, error) {
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if n > int64(limit) {
		_ = l.rdb.Decr(ctx, key).Err() // rollback
		return false, nil
	}
	return true, nil
}

func (l *RedisLimiter) Release(ctx context.Context, key string) error {
	return l.rdb.Decr(ctx, key).Err()
}
```

- [ ] **Step 5: Run test, verify pass**

Run: `cd go-server && go test ./internal/ratelimit/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/ratelimit/ go.mod go.sum
git commit -m "feat(ratelimit): Limiter interface + RedisLimiter (fixed-window + concurrency)"
```

---

## Task 4: Config — Redis + rate limit

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.RedisAddr string`, `Config.RateLimitRPM int`, `Config.RateLimitConcurrency int`.

- [ ] **Step 1: Viết test fail**

Thêm vào `config_test.go` (dùng `unsetenv` helper đã có trong package để không phụ thuộc env máy):

```go
func TestRateLimitDefaults(t *testing.T) {
	unsetenv(t, "AI_FACTORY_JWT_SECRET")
	t.Setenv("AI_FACTORY_REDIS_ADDR", "")
	t.Setenv("AI_FACTORY_RATE_LIMIT_RPM", "")
	t.Setenv("AI_FACTORY_RATE_LIMIT_CONCURRENCY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Fatalf("RedisAddr = %q, want localhost:6379", cfg.RedisAddr)
	}
	if cfg.RateLimitRPM != 60 || cfg.RateLimitConcurrency != 4 {
		t.Fatalf("limits = (%d,%d), want (60,4)", cfg.RateLimitRPM, cfg.RateLimitConcurrency)
	}
}

func TestRateLimitEnvOverride(t *testing.T) {
	t.Setenv("AI_FACTORY_JWT_SECRET", "0123456789abcdef")
	t.Setenv("AI_FACTORY_REDIS_ADDR", "redis:6379")
	t.Setenv("AI_FACTORY_RATE_LIMIT_RPM", "100")
	t.Setenv("AI_FACTORY_RATE_LIMIT_CONCURRENCY", "8")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RedisAddr != "redis:6379" || cfg.RateLimitRPM != 100 || cfg.RateLimitConcurrency != 8 {
		t.Fatalf("cfg = %+v", cfg)
	}
}
```

- [ ] **Step 2: Run test, verify fail**

Run: `cd go-server && go test ./internal/config/ -run TestRateLimit -v`
Expected: FAIL — field undefined.

- [ ] **Step 3: Implement**

Trong `config.go`, thêm 3 field vào struct `Config`:

```go
type Config struct {
	DatabaseURL          string // AI_FACTORY_DATABASE_URL (default: local dev compose)
	JWTSecret            string // AI_FACTORY_JWT_SECRET (default "dev-secret-change-me")
	LogLevel             string // AI_FACTORY_LOG_LEVEL (default "info")
	KafkaAddr            string // AI_FACTORY_KAFKA_ADDR (default "localhost:9092")
	RedisAddr            string // AI_FACTORY_REDIS_ADDR (default "localhost:6379")
	RateLimitRPM         int    // AI_FACTORY_RATE_LIMIT_RPM (default 60)
	RateLimitConcurrency int    // AI_FACTORY_RATE_LIMIT_CONCURRENCY (default 4)
}
```

Trong `Load()`, thêm vào struct literal:

```go
		RedisAddr:            env("AI_FACTORY_REDIS_ADDR", "localhost:6379"),
		RateLimitRPM:         envInt("AI_FACTORY_RATE_LIMIT_RPM", 60),
		RateLimitConcurrency: envInt("AI_FACTORY_RATE_LIMIT_CONCURRENCY", 4),
```

Thêm helper `envInt` (cạnh `env`) và import `strconv`:

```go
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `cd go-server && go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): RedisAddr + rate limit RPM/concurrency"
```

---

## Task 5: Handler — resolve + rate limit + fail-open

**Files:**
- Modify: `internal/api/handler.go`
- Test: `internal/api/handler_test.go`

**Interfaces:**
- Consumes: `auth.TenantIDFromContext` (Task 2), `ratelimit.Limiter` (Task 3), `controlplane.Deployment`/`ResolveDeployment`.
- Produces: `type DeploymentResolver interface { ResolveDeployment(ctx context.Context, tenantID, modelName string) (*controlplane.Deployment, error) }`; `func (h *Handler) resolveForTenant(ctx context.Context, w http.ResponseWriter, tenantID, model string) (*controlplane.Deployment, func(), bool)`; `NewHandler(...)` mở rộng signature.

- [ ] **Step 1: Viết test fail**

Thêm vào `handler_test.go` (package `api` nên truy cập được field/method unexported). Thêm imports `context`, `errors`, `time`, `"github.com/ai-factory/go-server/internal/controlplane"`:

```go
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
```

- [ ] **Step 2: Run test, verify fail**

Run: `cd go-server && go test ./internal/api/ -run TestResolveForTenant -v`
Expected: FAIL — `resolveForTenant` undefined, `Handler` thiếu field `resolver`/`limiter`/`rpmLimit`/`concLimit`.

- [ ] **Step 3: Implement**

Trong `handler.go`:

(3a) Thêm imports `log`, `time`, `"github.com/ai-factory/go-server/internal/controlplane"`, `"github.com/ai-factory/go-server/internal/ratelimit"`.

(3b) Thêm interface + mở rộng `Handler` + `NewHandler`:

```go
// DeploymentResolver resolves model name → READY deployment. Satisfied by *controlplane.Service.
type DeploymentResolver interface {
	ResolveDeployment(ctx context.Context, tenantID, modelName string) (*controlplane.Deployment, error)
}

type Handler struct {
	sessionMgr *session.Manager
	loop       *agent.Loop
	uiDir      string
	authSvc    *auth.Service
	secret     []byte
	resolver   DeploymentResolver
	limiter    ratelimit.Limiter
	rpmLimit   int
	concLimit  int
}

func NewHandler(sessionMgr *session.Manager, loop *agent.Loop, uiDir string, authSvc *auth.Service, secret []byte, resolver DeploymentResolver, limiter ratelimit.Limiter, rpmLimit, concLimit int) *Handler {
	return &Handler{
		sessionMgr: sessionMgr, loop: loop, uiDir: uiDir, authSvc: authSvc, secret: secret,
		resolver: resolver, limiter: limiter, rpmLimit: rpmLimit, concLimit: concLimit,
	}
}
```

(3c) Thêm `resolveForTenant` + helper fail-open (đặt trước `handleOpenAIChatCompletions`):

```go
// resolveForTenant resolves tenant+model → READY deployment, then applies RPM +
// concurrency limits (fail-open on Redis error). On success returns the
// deployment and a release func (for concurrency); on failure writes the error
// response and returns nil, nil, false.
func (h *Handler) resolveForTenant(ctx context.Context, w http.ResponseWriter, tenantID, model string) (*controlplane.Deployment, func(), bool) {
	d, err := h.resolver.ResolveDeployment(ctx, tenantID, model)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "no READY deployment for model")
		return nil, nil, false
	}
	if !h.allow(ctx, "tenant:"+tenantID+":rpm", h.rpmLimit, time.Minute) {
		writeOpenAIError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "rate limit exceeded")
		return nil, nil, false
	}
	if !h.acquire(ctx, "deployment:"+d.ID+":concurrency", h.concLimit) {
		writeOpenAIError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "concurrency limit exceeded")
		return nil, nil, false
	}
	release := func() { _ = h.limiter.Release(context.Background(), "deployment:"+d.ID+":concurrency") }
	return d, release, true
}

func (h *Handler) allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	ok, err := h.limiter.Allow(ctx, key, limit, window)
	if err != nil {
		log.Printf("rate limit allow error (fail-open): %v", err)
		return true
	}
	return ok
}

func (h *Handler) acquire(ctx context.Context, key string, limit int) bool {
	ok, err := h.limiter.Acquire(ctx, key, limit)
	if err != nil {
		log.Printf("rate limit acquire error (fail-open): %v", err)
		return true
	}
	return ok
}
```

(3d) Trong `handleOpenAIChatCompletions`, chèn sau `ValidateOpenAIRequest` (dòng 73), trước khối `sessionID := ...`:

```go
	tenantID, _ := auth.TenantIDFromContext(r.Context())
	_, release, ok := h.resolveForTenant(r.Context(), w, tenantID, req.Model)
	if !ok {
		return
	}
	defer release()
```

- [ ] **Step 4: Run test, verify pass**

Run: `cd go-server && go test ./internal/api/ -run TestResolveForTenant -v`
Expected: PASS. (`TestHandleUIRouting` vẫn pass vì dùng `&Handler{uiDir: dir}` trực tiếp, không qua `NewHandler`.)

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/api/handler.go internal/api/handler_test.go
git commit -m "feat(api): resolve model→deployment + rate limit (fail-open) on /v1/chat/completions"
```

---

## Task 6: Wiring (main.go) + seed demo + redis compose

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/seed.go`
- Modify: `deployments/docker-compose.yml`

**Interfaces:**
- Consumes: `ratelimit.NewRedisLimiter`, `redis.NewClient`, `config.Config` fields (Task 4), `api.NewHandler` mở rộng (Task 5), `controlplane.Service.ResolveDeployment` (Task 1).
- Produces: server boot với routing + rate limit hoạt động.

- [ ] **Step 1: Thêm service redis vào docker-compose**

Trong `deployments/docker-compose.yml`, thêm service (khớp style service postgres/kafka hiện có):

```yaml
  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
```

- [ ] **Step 2: Wire main.go**

Trong `main.go`:

(2a) Thêm imports `"github.com/redis/go-redis/v9"` và `"github.com/ai-factory/go-server/internal/ratelimit"`.

(2b) Sau khối seed (dòng 63–65), trước khi tạo handler, khởi tạo Redis client + limiter:

```go
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	limiter := ratelimit.NewRedisLimiter(rdb)
```

(2c) Đổi dòng `handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret))` thành:

```go
	handler := api.NewHandler(sessionMgr, loop, dir, authSvc, []byte(cfg.JWTSecret), cp, limiter, cfg.RateLimitRPM, cfg.RateLimitConcurrency)
```

(2d) Sau `seedAdmin` (dòng 65), gọi seed demo (best-effort, không fatal):

```go
	if err := seedDemo(ctx, cp); err != nil {
		log.Printf("WARN: seed demo deployment: %v", err)
	}
```

- [ ] **Step 3: Thêm `seedDemo` trong seed.go**

Thêm function (sau `seedAdmin`; `seed.go` đã import đủ `context`/`errors`/`fmt`/`log`/`os`/`controlplane`):

```go
// seedDemo seeds a demo model + READY deployment for the demo tenant so routing
// works without Kafka. Idempotent: skips if a READY deployment already resolves.
func seedDemo(ctx context.Context, cp *controlplane.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return err
	}
	var tenantID string
	for _, t := range tenants {
		if t.Name == tenantName {
			tenantID = t.ID
			break
		}
	}
	if tenantID == "" {
		return nil // no demo tenant yet; nothing to seed
	}
	if _, err := cp.ResolveDeployment(ctx, tenantID, "qwen-3b"); err == nil {
		return nil // already seeded
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return fmt.Errorf("resolve for seed: %w", err)
	}

	model, err := cp.CreateModel(ctx, controlplane.Model{Name: "qwen-3b", Task: "text-generation", Framework: "transformers"})
	if err != nil {
		return fmt.Errorf("seed model: %w", err)
	}
	mv, err := cp.CreateModelVersion(ctx, controlplane.ModelVersion{ModelID: model.ID, Version: "v1", ArtifactURI: "local://qwen-3b"})
	if err != nil {
		return fmt.Errorf("seed model version: %w", err)
	}
	tpl, err := cp.CreateTemplate(ctx, controlplane.ServingTemplate{Name: "transformers", Runtime: "transformers"})
	if err != nil {
		return fmt.Errorf("seed template: %w", err)
	}
	tv, err := cp.CreateTemplateVersion(ctx, controlplane.TemplateVersion{TemplateID: tpl.ID, Version: "v1", Image: "qwen-3b:latest"})
	if err != nil {
		return fmt.Errorf("seed template version: %w", err)
	}
	d, err := cp.CreateDeployment(ctx, controlplane.Deployment{
		TenantID: tenantID, ModelVersionID: mv.ID, TemplateVersionID: tv.ID,
		Name: "qwen-3b-prod", Region: "local", DesiredReplicas: 1,
	})
	if err != nil {
		return fmt.Errorf("seed deployment: %w", err)
	}
	for _, to := range []string{controlplane.DeploymentProvisioning, controlplane.DeploymentStarting, controlplane.DeploymentReady} {
		if _, err := cp.TransitionDeployment(ctx, d.ID, to); err != nil {
			return fmt.Errorf("seed transition to %s: %w", to, err)
		}
	}
	log.Printf("seeded demo model qwen-3b + READY deployment %s (tenant %s)", d.ID, tenantID)
	return nil
}
```

- [ ] **Step 4: Build + verify**

Run: `cd go-server && go build ./...`
Expected: build thành công (không lỗi import/signature).

Run: `cd go-server && go vet ./...`
Expected: sạch.

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add cmd/server/main.go cmd/server/seed.go deployments/docker-compose.yml
git commit -m "feat(server): wire redis limiter + seed demo model/deployment; add redis to compose"
```

---

## Task 7 (bonus D7): Metrics labels sau route resolve

> Bonus/optional — có thể bỏ task này độc lập, không ảnh hưởng các task khác. (Cơ chế: `statusRecorder` lưu labels, handler set labels qua interface `RouteLabelSetter` — không thể dùng request context vì handler không ghi được vào context của middleware.)

**Files:**
- Modify: `internal/observability/metrics.go`
- Modify: `internal/api/handler.go` (set labels sau resolve)
- Modify: `cmd/server/main.go` (`statusRecorder` + `metricsMiddleware`)

**Interfaces:**
- Produces: `observability.RouteLabelSetter interface { SetRouteLabels(tenant, deployment, model, region string) }`.

- [ ] **Step 1: Implement interface trong metrics.go**

Thêm vào `metrics.go` (không cần import mới):

```go
// RouteLabelSetter lets an HTTP handler record serving-domain labels on the
// metrics middleware's response recorder, so serving metrics resolve after
// routing (the handler knows tenant/deployment/model/region; the middleware
// only knows the HTTP status).
type RouteLabelSetter interface {
	SetRouteLabels(tenant, deployment, model, region string)
}
```

- [ ] **Step 2: Handler set labels**

Trong `handler.go`, `handleOpenAIChatCompletions`, đổi khối resolve (Task 5 step 3d) để bắt `d` và gọi `SetRouteLabels`. Thêm import `"github.com/ai-factory/go-server/internal/observability"`:

```go
	tenantID, _ := auth.TenantIDFromContext(r.Context())
	d, release, ok := h.resolveForTenant(r.Context(), w, tenantID, req.Model)
	if !ok {
		return
	}
	defer release()
	if ls, ok := w.(observability.RouteLabelSetter); ok {
		ls.SetRouteLabels(d.TenantID, d.ID, req.Model, d.Region)
	}
```

- [ ] **Step 3: statusRecorder + metricsMiddleware**

Trong `main.go`, mở rộng `statusRecorder` (thêm fields + method):

```go
type statusRecorder struct {
	http.ResponseWriter
	status     int
	tenant     string
	deployment string
	model      string
	region     string
}

func (r *statusRecorder) SetRouteLabels(tenant, deployment, model, region string) {
	r.tenant, r.deployment, r.model, r.region = tenant, deployment, model, region
}
```

Đổi `metricsMiddleware` (thay `""` bằng các field):

```go
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		status := strconv.Itoa(rec.status)
		observability.HTTPRequestsTotal.WithLabelValues(rec.tenant, rec.deployment, rec.model, rec.region, status).Inc()
		observability.RequestDurationSeconds.WithLabelValues(rec.tenant, rec.deployment, rec.model, rec.region, status).Observe(time.Since(start).Seconds())
	})
}
```

- [ ] **Step 4: Build + test**

Run: `cd go-server && go build ./... && go test ./internal/api/ -v`
Expected: build + test PASS.

- [ ] **Step 5: Commit**

```bash
cd /g/STUDY/AI/ai_factory/go-server
git add internal/observability/metrics.go internal/api/handler.go cmd/server/main.go
git commit -m "feat(metrics): fill tenant/deployment/model/region labels after route resolve"
```

---

## Task 8: Docs update

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/TRACKING.md`

- [ ] **Step 1: Cập nhật TRACKING.md**

- Dòng "⚠️ Việc treo" — đổi `Chưa có: rate-limit / persistence, sandbox cho run_command, observability` thành `Chưa có: persistence, sandbox cho run_command, observability (usage/tracing/cost)` (gỡ rate-limit).
- Thêm dòng vào "Nhật ký cập nhật": `2026-08-15 | M3 — inference routing (model→deployment READY, tenant isolation) + rate limit (Redis: tenant RPM + concurrency). Spec docs/superpowers/specs/2026-08-15-inference-routing-rate-limit-design.md.`

- [ ] **Step 2: Cập nhật CLAUDE.md**

- Mục "Architecture": thêm vào mô tả Go server — `routing (model→deployment READY, tenant-scoped)` và `rate limiter (Redis)`.
- Mục "Key Decisions": thêm 1 dòng — resolve theo `Model.name`; Redis fail-open; tenant RPM + deployment concurrency.
- Mục "Known Gaps": gỡ `rate-limit` khỏi "Not yet" (dòng "rate-limit/persistence" → "persistence").
- Mục "Running": thêm `redis` vào `docker compose up -d postgres kafka redis` + ghi chú seed demo model `qwen-3b`.

- [ ] **Step 3: Commit**

```bash
cd /g/STUDY/AI/ai_factory
git add CLAUDE.md docs/TRACKING.md
git commit -m "docs: M3 routing + rate limit — cập nhật roadmap, gaps, running"
```

---

## Self-Review Notes

- **Spec coverage:** Task 1 ↔ spec §3; Task 2 ↔ §3 (TenantIDFromContext); Task 3 ↔ §4; Task 4 ↔ §5; Task 5 ↔ §2/§6; Task 6 ↔ §5 (wiring) + D8 (seed); Task 7 ↔ D7; Task 8 ↔ §9 docs. Tất cả mục spec đều có task.
- **Non-goals giữ nguyên:** không multi-replica, không tpm/api-key/endpoint levels, không enforce quota, không đụng proto — không task nào vi phạm.
- **Type consistency:** `ResolveDeployment(ctx, tenantID, modelName)` xuyên suốt (Task 1 defines, Task 5/6 consume); `Limiter.Allow/Acquire/Release` (Task 3) khớp Task 5; `NewHandler(... resolver DeploymentResolver, limiter ratelimit.Limiter, rpmLimit, concLimit int)` khớp Task 5/6; config fields khớp Task 4/6; `RouteLabelSetter` (Task 7) khớp handler `w.(observability.RouteLabelSetter)` + `statusRecorder.SetRouteLabels`.
