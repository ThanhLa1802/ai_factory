# M1: Foundation + Control Plane — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Thiết lập nền tảng cho AI Factory thành serving platform: config + structured logging + metrics, PostgreSQL + migrations, auth (tenant/user/API key/JWT/RBAC), control plane CRUD (model, template, deployment + state machine, quota), và đóng gói Go server thành Docker image chạy trong cùng stack docker-compose.

**Architecture:** Giữ `go-server` làm module chính; thêm các package `config`, `observability`, `db`, `auth`, `controlplane` vào `go-server/internal/`. Thêm HTTP routes `/api/v1/*` cạnh các routes inference hiện có; một binary, worker goroutine (chưa cần ở M1). Python worker và proto **không đổi** ở M1.

**Tech Stack:** Go 1.25.6, pgx/v5, goose/v3, golang-jwt/v5, x/crypto (argon2), prometheus/client_golang, testify. Infra: docker-compose (postgres, redis, kafka, prometheus — chỉ postgres được dùng ở M1).

## Global Constraints

- Module path: `github.com/ai-factory/go-server`
- Tất cả entity ID là UUID (string, sinh bằng `github.com/google/uuid` — đã có sẵn)
- Tất cả timestamp là UTC (`TIMESTAMPTZ` trong DB, `time.Now().UTC()` trong Go)
- Error format JSON nhất quán: `{"error":{"code":"...","message":"..."}}`
- Roles: `PLATFORM_ADMIN`, `TENANT_ADMIN`, `TENANT_DEVELOPER`, `TENANT_VIEWER`
- Không log API key / password / raw prompt
- Migrations đặt tại `go-server/internal/db/migrations/` và embed vào binary (deviation khỏi spec §11 — migrations gốc để embed qua `go:embed`; ghi chú này đã chấp nhận)
- Thư mục làm việc cho lệnh chạy test/Go: `go-server/`
- Docker build context = **repo root** (`..`), Dockerfile tại `go-server/Dockerfile`, `.dockerignore` tại repo root — image cần copy `ui/` (nằm ngoài `go-server/`)
- Python worker **không** container hóa (GPU local RTX 3060, model 5.9GB) — container kết nối qua `host.docker.internal:50051`; `grpc.NewClient` lazy nên container vẫn khởi động khi worker chưa chạy

---

### Task 1: Foundation — config, structured logging, metrics

**Files:**
- Create: `go-server/internal/config/config.go`
- Create: `go-server/internal/observability/log.go`
- Create: `go-server/internal/observability/metrics.go`
- Modify: `go-server/cmd/server/main.go`

**Interfaces:**
- Consumes: (không — task đầu)
- Produces: `config.Config` (fields `DatabaseURL string`, `JWTSecret string`, `LogLevel string`); `observability.SetupLogger(level string) *slog.Logger`; `observability.MetricsHandler() http.Handler`; metrics `observability.HTTPRequestsTotal`, `observability.RequestDurationSeconds`

- [ ] **Step 1: Write failing test for config.Load()**

Create `go-server/internal/config/config.go` with an empty `Load()` stub returning `nil, nil`, then write the test:

Create `go-server/internal/config/config.go`:
```go
package config

func Load() (*Config, error) {
	return nil, nil
}
```

Create `go-server/internal/config/config_test.go`:
```go
package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "")
	t.Setenv("AI_FACTORY_JWT_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.JWTSecret == "" {
		t.Error("JWTSecret empty, want non-empty default for dev")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("AI_FACTORY_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("AI_FACTORY_JWT_SECRET", "0123456789abcdef")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://u:p@localhost:5432/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != "0123456789abcdef" {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/config/...`
Expected: FAIL — `Load()` trả `(nil, nil)` → test truy cập `cfg.LogLevel` trên nil pointer (panic) — test chắc chắn fail.

- [ ] **Step 3: Implement config.Load()**

Replace content of `go-server/internal/config/config.go`:
```go
package config

import (
	"fmt"
	"os"
)

// Config holds runtime configuration, read from environment variables.
// The HTTP port stays on the existing --port flag in cmd/server/main.go.
type Config struct {
	DatabaseURL string // AI_FACTORY_DATABASE_URL (default: local dev compose)
	JWTSecret   string // AI_FACTORY_JWT_SECRET (default "dev-secret-change-me")
	LogLevel    string // AI_FACTORY_LOG_LEVEL (default "info")
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	secret := env("AI_FACTORY_JWT_SECRET", "dev-secret-change-me")
	if len(secret) < 16 {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET must be at least 16 characters")
	}
	return &Config{
		DatabaseURL: env("AI_FACTORY_DATABASE_URL", "postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable"),
		JWTSecret:   secret,
		LogLevel:    env("AI_FACTORY_LOG_LEVEL", "info"),
	}, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go-server && go test ./internal/config/...`
Expected: PASS

- [ ] **Step 5: Write failing test for slog setup**

Create `go-server/internal/observability/log_test.go`:
```go
package observability

import (
	"testing"
)

func TestSetupLoggerJSON(t *testing.T) {
	logger := SetupLogger("debug")
	if logger == nil {
		t.Fatal("SetupLogger returned nil")
	}
}
```

Create `go-server/internal/observability/log.go` (stub):
```go
package observability

import "log/slog"

func SetupLogger(level string) *slog.Logger { return nil }
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd go-server && go test ./internal/observability/...`
Expected: FAIL — `SetupLogger returned nil`

- [ ] **Step 7: Implement SetupLogger**

Replace `go-server/internal/observability/log.go`:
```go
package observability

import (
	"log/slog"
	"os"
)

// SetupLogger configures the process-wide slog logger to emit JSON.
func SetupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(logger)
	return logger
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd go-server && go test ./internal/observability/...`
Expected: PASS

- [ ] **Step 9: Write failing test for metrics registry**

Create `go-server/internal/observability/metrics_test.go`:
```go
package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	_, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if _, err := HTTPRequestsTotal.GetMetricWithLabelValues("test_tenant", "deploy_1", "model_1", "vn", "200"); err != nil {
		t.Fatalf("label combo rejected: %v", err)
	}
}
```

Create `go-server/internal/observability/metrics.go` (stub):
```go
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
)

var _ = prometheus.NewCounterVec
```

- [ ] **Step 10: Run test to verify it fails**

Run: `cd go-server && go test ./internal/observability/...`
Expected: FAIL — `undefined: Registry`, `undefined: HTTPRequestsTotal`

- [ ] **Step 11: Implement metrics.go**

Replace `go-server/internal/observability/metrics.go`:
```go
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registry = prometheus.NewRegistry()

	// HTTPRequestsTotal counts inference + control plane requests.
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "serving_requests_total",
			Help: "Total HTTP requests served.",
		},
		[]string{"tenant", "deployment", "model", "region", "status"},
	)

	// RequestDurationSeconds measures handler latency.
	RequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "serving_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tenant", "deployment", "model", "region", "status"},
	)
)

func init() {
	registry.MustRegister(HTTPRequestsTotal, RequestDurationSeconds)
}

// Registry exposes the app Prometheus registry.
func Registry() *prometheus.Registry { return registry }

// MetricsHandler returns an HTTP handler exposing /metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
```

- [ ] **Step 12: Run test to verify it passes**

Run: `cd go-server && go get github.com/prometheus/client_golang@latest && go test ./internal/observability/...`
Expected: PASS

- [ ] **Step 13: Wire config + slog + metrics into main.go**

Modify `go-server/cmd/server/main.go`. Add imports and wire at the top of `main()`:
```go
import (
	"context"
	...
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/observability"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	observability.SetupLogger(cfg.LogLevel)
	log.Printf("=== AI Factory Server ===")
	log.Printf("HTTP port: %d", *httpPort) // flag --port hiện có, không đổi
	...
	// Mount /metrics on the same mux used by RegisterRoutes.
	// (In the existing code, `mux` is created in main; add:)
	mux.Handle("/metrics", observability.MetricsHandler())
}
```
Giữ nguyên logic hiện tại (gRPC client connect, session manager, loop) và flag `--port` hiện có. Chỉ thêm: config.Load() ở đầu `main()`, `slog.SetDefault` qua `observability.SetupLogger`, và mount `/metrics`. Sau khi sửa, chạy:

- [ ] **Step 14: Verify build passes**

Run: `cd go-server && go build ./...`
Expected: build success (metric handler mount phải được thêm đúng chỗ `mux` tồn tại; nếu main chưa có mux rõ ràng, tạo `mux := http.NewServeMux()` rồi mount).

- [ ] **Step 15: Commit**

```bash
cd go-server && git add internal/config internal/observability cmd/server/main.go
git commit -m "feat(server): config + slog + prometheus metrics foundation"
```

---

### Task 2: PostgreSQL — pool, embedded migrations, schema, docker-compose

**Files:**
- Create: `go-server/internal/db/db.go`
- Create: `go-server/internal/db/db_test.go`
- Create: `go-server/internal/db/migrations/0001_init.sql`
- Create: `deployments/docker-compose.yml`

**Interfaces:**
- Consumes: nothing from other tasks
- Produces: `db.Connect(ctx, dsn) (*DB, error)`; `(*DB).Pool() *pgxpool.Pool`; `(*DB).Migrate(ctx) error`; full M1 schema (bảng dưới)

- [ ] **Step 1: Write the migration schema**

Create `go-server/internal/db/migrations/0001_init.sql`:
```sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    status     TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenant_memberships (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tenant_id)
);

CREATE TABLE api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    status       TEXT NOT NULL DEFAULT 'ACTIVE',
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenant_quotas (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    quota_type  TEXT NOT NULL,              -- tokens | requests | gpu_hours | concurrency
    limit_value BIGINT NOT NULL,
    period      TEXT NOT NULL,              -- day | month
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, quota_type, period)
);

CREATE TABLE models (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    task        TEXT NOT NULL,
    framework   TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name, framework)
);

CREATE TABLE model_versions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id     UUID NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    version      TEXT NOT NULL,
    artifact_uri TEXT NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model_id, version)
);

CREATE TABLE serving_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    runtime     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE serving_template_versions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id   UUID NOT NULL REFERENCES serving_templates(id) ON DELETE CASCADE,
    version       TEXT NOT NULL,
    image         TEXT NOT NULL,
    command       JSONB NOT NULL DEFAULT '[]',
    environment   JSONB NOT NULL DEFAULT '{}',
    config_schema JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (template_id, version)
);

CREATE TABLE deployments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    model_version_id    UUID NOT NULL REFERENCES model_versions(id),
    template_version_id UUID NOT NULL REFERENCES serving_template_versions(id),
    name                TEXT NOT NULL,
    region              TEXT NOT NULL,
    desired_replicas    INT NOT NULL DEFAULT 1,
    status              TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE deployment_revisions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    revision      INT NOT NULL,
    spec_json     JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by    UUID REFERENCES users(id),
    UNIQUE (deployment_id, revision)
);

CREATE TABLE endpoints (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    path          TEXT NOT NULL,
    protocol      TEXT NOT NULL DEFAULT 'http',
    status        TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS endpoints, deployment_revisions, deployments,
    serving_template_versions, serving_templates, model_versions, models,
    tenant_quotas, api_keys, tenant_memberships, users, tenants CASCADE;
```

- [ ] **Step 2: Write db.go with embed**

Create `go-server/internal/db/db.go`:
```go
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
	dsn  string
}

// Connect opens a pgx pool and verifies the connection.
func Connect(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool, dsn: dsn}, nil
}

// Pool exposes the underlying pgx pool.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Migrate applies all embedded goose migrations.
func (d *DB) Migrate(ctx context.Context) error {
	sqlDB, err := sql.Open("pgx", d.dsn)
	if err != nil {
		return fmt.Errorf("open sql db: %w", err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrationsFS, goose.WithVerbose(false))
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Write integration test (gated by env)**

Create `go-server/internal/db/db_test.go`:
```go
package db

import (
	"context"
	"os"
	"testing"
)

// TestMigrateAndPing runs only when AI_FACTORY_DATABASE_URL is set (integration).
func TestMigrateAndPing(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := d.Pool().Ping(ctx); err != nil {
		t.Fatalf("Ping() after migrate error = %v", err)
	}
}
```

- [ ] **Step 4: Create docker-compose**

Create `deployments/docker-compose.yml`:
```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: ai_factory
      POSTGRES_PASSWORD: ai_factory
      POSTGRES_DB: ai_factory
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ai_factory -d ai_factory"]
      interval: 5s
      timeout: 3s
      retries: 10

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"

  kafka:
    image: bitnami/kafka:3.7
    environment:
      KAFKA_CFG_NODE_ID: "1"
      KAFKA_CFG_PROCESS_ROLES: "broker,controller"
      KAFKA_CFG_CONTROLLER_QUORUM_VOTERS: "1@localhost:9093"
      KAFKA_CFG_LISTENERS: "PLAINTEXT://:9092,CONTROLLER://:9093"
      KAFKA_CFG_ADVERTISED_LISTENERS: "PLAINTEXT://localhost:9092"
      KAFKA_CFG_CONTROLLER_LISTENER_NAMES: "CONTROLLER"
    ports:
      - "9092:9092"

  prometheus:
    image: prom/prometheus:v2.53.0
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro

volumes:
  pgdata:
```

Create `deployments/prometheus.yml`:
```yaml
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: ai-factory
    static_configs:
      - targets: ["host.docker.internal:8080"]
```

- [ ] **Step 5: Install deps and run unit-safe build**

Run: `cd go-server && go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest && go build ./...`
Expected: build success

- [ ] **Step 6: Verify migrations against a live Postgres (manual)**

Run: `docker compose -f deployments/docker-compose.yml up -d postgres`
Then: `cd go-server && AI_FACTORY_DATABASE_URL='postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable' go test ./internal/db/ -run TestMigrateAndPing -v`
Expected: PASS (migration applied; có thể kiểm tra `\dt` trong postgres để thấy 12 bảng)

- [ ] **Step 7: Commit**

```bash
cd go-server && git add internal/db deployments/ && git commit -m "feat(db): pgx pool + goose migrations (M1 schema) + docker-compose"
```

---

### Task 3: Auth primitives — password, JWT, API key

**Files:**
- Create: `go-server/internal/auth/password.go`, `password_test.go`
- Create: `go-server/internal/auth/jwt.go`, `jwt_test.go`
- Create: `go-server/internal/auth/apikey.go`, `apikey_test.go`
- Create: `go-server/internal/auth/rbac.go`, `rbac_test.go`

**Interfaces:**
- Consumes: `google/uuid` (đã có)
- Produces: `auth.HashPassword(plain) (string, error)`, `auth.VerifyPassword(hash, plain) bool`; `auth.IssueToken(secret []byte, userID, tenantID, role string, ttl time.Duration) (string, error)`, `auth.ParseToken(secret []byte, token string) (*Claims, error)`, `auth.Claims{UserID, TenantID, Role string; jwt.RegisteredClaims}`; `auth.GenerateAPIKey() (raw, hash string)`, `auth.HashAPIKey(raw) string`; constants `auth.RolePlatformAdmin`, `auth.RoleTenantAdmin`, `auth.RoleTenantDeveloper`, `auth.RoleTenantViewer`; `auth.RoleAllows(role, action string) bool`

- [ ] **Step 1: Password hash — failing test**

Create `go-server/internal/auth/password.go` (stub):
```go
package auth

func HashPassword(plain string) (string, error) { return "", nil }
func VerifyPassword(hash, plain string) bool    { return false }
```

Create `go-server/internal/auth/password_test.go`:
```go
package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	if hash == "hunter2" {
		t.Fatal("hash must not be plaintext")
	}
	if !VerifyPassword(hash, "hunter2") {
		t.Error("VerifyPassword(correct) = false, want true")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("VerifyPassword(wrong) = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run TestHashAndVerify`
Expected: FAIL — `VerifyPassword(correct) = false, want true`

- [ ] **Step 3: Implement password.go**

Replace `go-server/internal/auth/password.go`:
```go
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// HashPassword returns an argon2id PHC string: argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	hash := argon2.IDKey([]byte(plain), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword checks a plaintext password against an argon2id PHC string.
func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(plain), salt, 1, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go-server && go get golang.org/x/crypto@latest && go test ./internal/auth/ -run TestHashAndVerify`
Expected: PASS

- [ ] **Step 5: JWT — failing test**

Create `go-server/internal/auth/jwt.go` (stub):
```go
package auth

import "time"

type Claims struct {
	UserID   string `json:"uid"`
	TenantID string `json:"tid"`
	Role     string `json:"role"`
}

func IssueToken(secret []byte, userID, tenantID, role string, ttl time.Duration) (string, error) {
	return "", nil
}

func ParseToken(secret []byte, token string) (*Claims, error) {
	return nil, nil
}
```

Create `go-server/internal/auth/jwt_test.go`:
```go
package auth

import (
	"testing"
	"time"
)

func TestIssueAndParse(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, err := IssueToken(secret, "u1", "t1", RoleTenantAdmin, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken error = %v", err)
	}
	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken error = %v", err)
	}
	if claims.UserID != "u1" || claims.TenantID != "t1" || claims.Role != RoleTenantAdmin {
		t.Errorf("claims = %+v", claims)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	token, _ := IssueToken([]byte("0123456789abcdef"), "u1", "t1", RoleTenantViewer, time.Hour)
	if _, err := ParseToken([]byte("fedcba9876543210"), token); err == nil {
		t.Error("expected error for wrong secret")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run TestIssueAndParse`
Expected: FAIL — `ParseToken error = ...` (token rỗng)

- [ ] **Step 7: Implement jwt.go**

Replace `go-server/internal/auth/jwt.go`:
```go
package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the JWT payload.
type Claims struct {
	UserID   string `json:"uid"`
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken mints a signed HS256 access token.
func IssueToken(secret []byte, userID, tenantID, role string, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := &Claims{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// ParseToken verifies and parses an access token.
func ParseToken(secret []byte, token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	return claims, nil
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd go-server && go get github.com/golang-jwt/jwt/v5@latest && go test ./internal/auth/ -run TestIssueAndParse`
Expected: PASS

- [ ] **Step 9: API key — failing test**

Create `go-server/internal/auth/apikey.go` (stub):
```go
package auth

func GenerateAPIKey() (string, string) { return "", "" }
func HashAPIKey(raw string) string     { return "" }
```

Create `go-server/internal/auth/apikey_test.go`:
```go
package auth

import "testing"

func TestGenerateAPIKey(t *testing.T) {
	raw, hash := GenerateAPIKey()
	if len(raw) < 20 {
		t.Fatalf("raw key too short: %q", raw)
	}
	if HashAPIKey(raw) != hash {
		t.Error("HashAPIKey(raw) != returned hash")
	}
	if HashAPIKey("different") == hash {
		t.Error("hash collision with different input")
	}
}
```

- [ ] **Step 10: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run TestGenerateAPIKey`
Expected: FAIL — `raw key too short`

- [ ] **Step 11: Implement apikey.go**

Replace `go-server/internal/auth/apikey.go`:
```go
package auth

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

const keyPrefix = "sk-"

// GenerateAPIKey returns (raw secret, hash). The raw secret is shown once.
func GenerateAPIKey() (string, string) {
	raw := keyPrefix + uuid.NewString() + uuid.NewString()
	return raw, HashAPIKey(raw)
}

// HashAPIKey returns the hex sha256 of a raw API key. Only the hash is stored.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 12: Run test to verify it passes**

Run: `cd go-server && go test ./internal/auth/ -run TestGenerateAPIKey`
Expected: PASS

- [ ] **Step 13: RBAC — failing test**

Create `go-server/internal/auth/rbac.go` (stub):
```go
package auth

const (
	RolePlatformAdmin   = "PLATFORM_ADMIN"
	RoleTenantAdmin     = "TENANT_ADMIN"
	RoleTenantDeveloper = "TENANT_DEVELOPER"
	RoleTenantViewer    = "TENANT_VIEWER"
)

func RoleAllows(role, action string) bool { return false }
```

Create `go-server/internal/auth/rbac_test.go`:
```go
package auth

import "testing"

func TestRoleAllows(t *testing.T) {
	cases := []struct {
		role, action string
		want         bool
	}{
		{RoleTenantViewer, "deployment.read", true},
		{RoleTenantViewer, "deployment.write", false},
		{RoleTenantDeveloper, "deployment.write", true},
		{RoleTenantDeveloper, "key.manage", false},
		{RoleTenantAdmin, "key.manage", true},
		{RoleTenantAdmin, "template.write", false},
		{RolePlatformAdmin, "template.write", true},
		{RolePlatformAdmin, "tenant.manage", true},
		{"UNKNOWN", "deployment.read", false},
	}
	for _, c := range cases {
		if got := RoleAllows(c.role, c.action); got != c.want {
			t.Errorf("RoleAllows(%q, %q) = %v, want %v", c.role, c.action, got, c.want)
		}
	}
}
```

- [ ] **Step 14: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run TestRoleAllows`
Expected: FAIL

- [ ] **Step 15: Implement rbac.go**

Replace `go-server/internal/auth/rbac.go`:
```go
package auth

const (
	RolePlatformAdmin   = "PLATFORM_ADMIN"
	RoleTenantAdmin     = "TENANT_ADMIN"
	RoleTenantDeveloper = "TENANT_DEVELOPER"
	RoleTenantViewer    = "TENANT_VIEWER"
)

// Action constants used by RequirePermission middleware.
const (
	ActionTenantManage   = "tenant.manage"
	ActionTenantRead     = "tenant.read"
	ActionModelWrite     = "model.write"
	ActionModelRead      = "model.read"
	ActionTemplateWrite  = "template.write"
	ActionTemplateRead   = "template.read"
	ActionDeployWrite    = "deployment.write"
	ActionDeployRead     = "deployment.read"
	ActionKeyManage      = "key.manage"
	ActionQuotaManage    = "quota.manage"
	ActionUsageRead      = "usage.read"
)

var rolePermissions = map[string]map[string]bool{
	RolePlatformAdmin: {
		ActionTenantManage: true, ActionTenantRead: true,
		ActionModelWrite: true, ActionModelRead: true,
		ActionTemplateWrite: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionKeyManage: true, ActionQuotaManage: true, ActionUsageRead: true,
	},
	RoleTenantAdmin: {
		ActionTenantRead: true, ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionKeyManage: true, ActionQuotaManage: true, ActionUsageRead: true,
	},
	RoleTenantDeveloper: {
		ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployWrite: true, ActionDeployRead: true,
		ActionUsageRead: true,
	},
	RoleTenantViewer: {
		ActionModelRead: true, ActionTemplateRead: true,
		ActionDeployRead: true, ActionUsageRead: true,
	},
}

// RoleAllows reports whether role may perform action.
func RoleAllows(role, action string) bool {
	return rolePermissions[role][action]
}
```

- [ ] **Step 16: Run test to verify it passes**

Run: `cd go-server && go test ./internal/auth/...`
Expected: PASS (cả 4 test)

- [ ] **Step 17: Commit**

```bash
cd go-server && git add internal/auth && git commit -m "feat(auth): argon2 password + JWT + API key + RBAC primitives"
```

---

### Task 4: Control plane service — tenants, users, memberships, API keys storage

**Files:**
- Create: `go-server/internal/controlplane/types.go`
- Create: `go-server/internal/controlplane/users.go` (tenant + user + membership + api_key queries)
- Create: `go-server/internal/controlplane/users_test.go`

**Interfaces:**
- Consumes: `db.DB` (Task 2), `auth` role constants (Task 3)
- Produces: types `controlplane.Tenant{ID,Name,Status string; CreatedAt,UpdatedAt time.Time}`, `controlplane.User{ID,Username,Email,Role,TenantID,Status string}`, `controlplane.APIKey{ID,TenantID,Name,Status string; ExpiresAt *time.Time; CreatedAt time.Time}`; `controlplane.Service{db *pgxpool.Pool}`; methods `NewService(pool *pgxpool.Pool) *Service`, `CreateTenant(ctx,name) (*Tenant,error)`, `ListTenants(ctx) ([]Tenant,error)`, `CreateUser(ctx, username,email,passwordHash,role,tenantID string) (*User,error)`, `GetUserByUsername(ctx, username string) (*User, string, error)` (trả password_hash), `CreateAPIKey(ctx, tenantID,name,keyHash string, expiresAt *time.Time) (*APIKey,error)`, `GetAPIKeyByHash(ctx, keyHash string) (*APIKey,error)`, `GetUserMembership(ctx, userID string) (tenantID, role string, err error)`

- [ ] **Step 1: Write types.go**

Create `go-server/internal/controlplane/types.go`:
```go
package controlplane

import "time"

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"`
}

type APIKey struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
```

- [ ] **Step 2: Write failing service test**

Create `go-server/internal/controlplane/users_test.go`:
```go
package controlplane

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/ai-factory/go-server/internal/db"
)

// SQL generation is validated through the integration test below (gated by env)
// and the E2E in Task 9. TestNewService is the fail-first gate for the stub.
func TestNewService(t *testing.T) {
	svc := NewService(nil)
	if svc == nil {
		t.Fatal("NewService(nil) returned nil")
	}
}

func TestUsesUUID(t *testing.T) {
	if id := uuid.NewString(); len(id) != 36 {
		t.Errorf("uuid length = %d, want 36", len(id))
	}
}

// TestTenantUserAPIKeyIntegration runs against a live Postgres (set AI_FACTORY_DATABASE_URL).
func TestTenantUserAPIKeyIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewService(d.Pool())

	tenant, err := svc.CreateTenant(ctx, "it-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateTenant error = %v", err)
	}
	user, err := svc.CreateUser(ctx, "it-user", "it@example.com", "hash", "TENANT_ADMIN", tenant.ID)
	if err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	if user.TenantID != tenant.ID {
		t.Errorf("user.TenantID = %q, want %q", user.TenantID, tenant.ID)
	}
	got, hash, err := svc.GetUserByUsername(ctx, "it-user")
	if err != nil || got.ID != user.ID || hash != "hash" {
		t.Errorf("GetUserByUsername = (%+v, %q, %v)", got, hash, err)
	}
	key, err := svc.CreateAPIKey(ctx, tenant.ID, "it-key", "abc123hash", nil)
	if err != nil {
		t.Fatalf("CreateAPIKey error = %v", err)
	}
	found, err := svc.GetAPIKeyByHash(ctx, "abc123hash")
	if err != nil || found.ID != key.ID {
		t.Errorf("GetAPIKeyByHash = (%+v, %v)", found, err)
	}
}
```

Create `go-server/internal/controlplane/users.go` (stub):
```go
package controlplane

import "github.com/jackc/pgx/v5/pgxpool"

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service { return nil }
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go-server && go test ./internal/controlplane/ -run TestNewService`
Expected: FAIL — `NewService(nil) returned nil`

- [ ] **Step 4: Implement Service + queries**

Replace `go-server/internal/controlplane/users.go`:
```go
package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is the control plane use-case layer. All methods write to PostgreSQL.
type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// --- tenants ---

func (s *Service) CreateTenant(ctx context.Context, name string) (*Tenant, error) {
	t := &Tenant{ID: uuid.NewString(), Name: name, Status: "ACTIVE"}
	err := s.db.QueryRow(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2)
		 RETURNING status, created_at, updated_at`,
		t.ID, t.Name).Scan(&t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}

func (s *Service) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, status, created_at, updated_at FROM tenants ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	out := []Tenant{}
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- users + memberships ---

func (s *Service) CreateUser(ctx context.Context, username, email, passwordHash, role, tenantID string) (*User, error) {
	u := &User{ID: uuid.NewString(), Username: username, Email: email, Role: role, TenantID: tenantID, Status: "ACTIVE"}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1, $2, $3, $4)`,
		u.ID, u.Username, u.Email, passwordHash); err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant_memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)`,
		u.ID, tenantID, role); err != nil {
		return nil, fmt.Errorf("insert membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return u, nil
}

// GetUserByUsername returns the user and its password hash for verification.
func (s *Service) GetUserByUsername(ctx context.Context, username string) (*User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx,
		`SELECT u.id, u.username, u.email, u.status, m.role, m.tenant_id, u.password_hash
		 FROM users u
		 JOIN tenant_memberships m ON m.user_id = u.id
		 WHERE u.username = $1`, username).
		Scan(&u.ID, &u.Username, &u.Email, &u.Status, &u.Role, &u.TenantID, &hash)
	if err != nil {
		return nil, "", fmt.Errorf("get user: %w", err)
	}
	return &u, hash, nil
}

// --- api keys ---

func (s *Service) CreateAPIKey(ctx context.Context, tenantID, name, keyHash string, expiresAt *time.Time) (*APIKey, error) {
	k := &APIKey{ID: uuid.NewString(), TenantID: tenantID, Name: name, Status: "ACTIVE", ExpiresAt: expiresAt}
	err := s.db.QueryRow(ctx,
		`INSERT INTO api_keys (id, tenant_id, name, key_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at`,
		k.ID, k.TenantID, k.Name, keyHash, expiresAt).Scan(&k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return k, nil
}

func (s *Service) GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	var k APIKey
	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, name, status, expires_at, created_at
		 FROM api_keys WHERE key_hash = $1`, keyHash).
		Scan(&k.ID, &k.TenantID, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return &k, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go-server && go test ./internal/controlplane/ -run TestNewService`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd go-server && git add internal/controlplane && git commit -m "feat(controlplane): tenant/user/api_key storage service"
```

---

### Task 5: Auth service (login, API key auth) + HTTP middleware

**Files:**
- Create: `go-server/internal/auth/service.go`, `service_test.go`
- Create: `go-server/internal/auth/middleware.go`, `middleware_test.go`

**Interfaces:**
- Consumes: `controlplane.User`, `controlplane.APIKey`, `controlplane.Service` (Task 4) — `*controlplane.Service` implements the `auth.Store` interface below; auth primitives (Task 3)
- Produces: interface `auth.Store{GetUserByUsername(ctx, username) (*controlplane.User, string, error); GetAPIKeyByHash(ctx, keyHash) (*controlplane.APIKey, error)}`; `auth.Service{store Store; secret []byte; tokenTTL time.Duration}`; `auth.NewService(store Store, secret []byte, ttl time.Duration) *Service`; `(*Service).Login(ctx, username, password string) (string, error)` (returns `ErrInvalidCredentials`); `(*Service).AuthenticateAPIKey(ctx, rawKey string) (*controlplane.APIKey, error)` (returns `ErrInvalidAPIKey` / `ErrKeyInactive`); middleware `RequireAuth(secret []byte) func(http.Handler) http.Handler`, `RequirePermission(secret []byte, action string) func(http.Handler) http.Handler`, `ClaimsFromContext(ctx) (*Claims, bool)`

- [ ] **Step 1: Write failing Login test with a fake store**

Because `Login`/`AuthenticateAPIKey` call Postgres, inject a `Store` interface so the decision logic is unit-testable with a fake. Create `go-server/internal/auth/service_test.go`:
```go
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
	store := &fakeStore{user: &controlplane.User{ID: "u1", TenantID: "t1", Role: RoleTenantAdmin}, hash: hash}
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

func TestLoginWrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	store := &fakeStore{user: &controlplane.User{ID: "u1", TenantID: "t1", Role: RoleTenantViewer}, hash: hash}
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
```

Create `go-server/internal/auth/service.go` (stub — defines types so the test compiles):
```go
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

type Store interface {
	GetUserByUsername(ctx context.Context, username string) (*controlplane.User, string, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*controlplane.APIKey, error)
}

type Service struct {
	store    Store
	secret   []byte
	tokenTTL time.Duration
}

func NewService(store Store, secret []byte, ttl time.Duration) *Service {
	return nil
}

func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	return "", nil
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey string) (*controlplane.APIKey, error) {
	return nil, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run 'TestLogin|TestAuthenticate'`
Expected: FAIL — `Login` trả về chuỗi rỗng → `ParseToken` lỗi, hoặc nil dereference vì `NewService` trả nil.

- [ ] **Step 3: Implement service.go**

Replace `go-server/internal/auth/service.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go-server && go test ./internal/auth/ -run 'TestLogin|TestAuthenticate'`
Expected: PASS

- [ ] **Step 5: Write middleware failing test**

Create `go-server/internal/auth/middleware.go` (stub):
```go
package auth

import (
	"context"
	"net/http"
)

func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

func RequirePermission(secret []byte, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	return nil, false
}
```

Create `go-server/internal/auth/middleware_test.go`:
```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequireAuthAcceptsValidToken(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)

	handler := RequireAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "no claims", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(claims.TenantID))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "t1" {
		t.Errorf("body = %q, want t1", rec.Body.String())
	}
}

func TestRequireAuthRejectsMissing(t *testing.T) {
	secret := []byte("0123456789abcdef")
	handler := RequireAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestRequirePermissionDeniesViewerWrite(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, _ := IssueToken(secret, "u1", "t1", RoleTenantViewer, time.Hour)
	handler := RequirePermission(secret, ActionDeployWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd go-server && go test ./internal/auth/ -run TestRequireAuth`
Expected: FAIL — `no claims`/body empty (middleware chưa parse token)

- [ ] **Step 7: Implement middleware.go**

Replace `go-server/internal/auth/middleware.go`:
```go
package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// RequireAuth validates a Bearer JWT and stores *Claims in the context.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
				return
			}
			claims, err := ParseToken(secret, strings.TrimPrefix(h, prefix))
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
		})
	}
}

// RequirePermission wraps RequireAuth and additionally checks the role's action.
func RequirePermission(secret []byte, action string) func(http.Handler) http.Handler {
	requireAuth := RequireAuth(secret)
	return func(next http.Handler) http.Handler {
		return requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok || !RoleAllows(claims.Role, action) {
				writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "permission denied")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// ClaimsFromContext extracts the authenticated claims.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + msg + `"}}`))
}
```

- [ ] **Step 8: Run middleware tests**

Run: `cd go-server && go test ./internal/auth/...`
Expected: PASS (cả auth package)

- [ ] **Step 9: Commit**

```bash
cd go-server && git add internal/auth && git commit -m "feat(auth): login + API key auth service + JWT/RBAC middleware"
```

---

### Task 6: Model registry + serving templates (CRUD storage)

**Files:**
- Create: `go-server/internal/controlplane/model.go` (entities Model, ModelVersion, ServingTemplate, TemplateVersion)
- Create: `go-server/internal/controlplane/catalog.go` (queries: models, versions, templates, template versions)
- Create: `go-server/internal/controlplane/catalog_test.go`

**Interfaces:**
- Consumes: `Service` struct (Task 4)
- Produces: methods on `*Service`: `CreateModel(ctx, m Model) (*Model, error)`, `ListModels(ctx) ([]Model, error)`, `GetModel(ctx, id string) (*Model, error)`, `CreateModelVersion(ctx, mv ModelVersion) (*ModelVersion, error)`, `CreateTemplate(ctx, t ServingTemplate) (*ServingTemplate, error)`, `ListTemplates(ctx) ([]ServingTemplate, error)`, `GetTemplate(ctx, id string) (*ServingTemplate, error)`, `CreateTemplateVersion(ctx, tv TemplateVersion) (*TemplateVersion, error)`. Entities in `model.go`.

- [ ] **Step 1: Write entities**

Create `go-server/internal/controlplane/model.go`:
```go
package controlplane

import "time"

type Model struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Task        string    `json:"task"`
	Framework   string    `json:"framework"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ModelVersion struct {
	ID          string         `json:"id"`
	ModelID     string         `json:"model_id"`
	Version     string         `json:"version"`
	ArtifactURI string         `json:"artifact_uri"`
	Metadata    map[string]any `json:"metadata"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
}

type ServingTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Runtime     string    `json:"runtime"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TemplateVersion struct {
	ID           string            `json:"id"`
	TemplateID   string            `json:"template_id"`
	Version      string            `json:"version"`
	Image        string            `json:"image"`
	Command      []string          `json:"command"`
	Environment  map[string]string `json:"environment"`
	ConfigSchema map[string]any    `json:"config_schema"`
	CreatedAt    time.Time         `json:"created_at"`
}
```

- [ ] **Step 2: Write failing test for JSON round-trip**

Create `go-server/internal/controlplane/catalog_test.go`:
```go
package controlplane

import (
	"encoding/json"
	"testing"
)

func TestModelRoundTrip(t *testing.T) {
	m := Model{ID: "m1", Name: "deepseek-v3", Task: "text-generation", Framework: "vllm"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Model
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Name != "deepseek-v3" || out.Framework != "vllm" {
		t.Errorf("out = %+v", out)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go-server && go test ./internal/controlplane/ -run TestModelRoundTrip`
Expected: FAIL — `undefined: Model`

- [ ] **Step 4: Implement catalog.go**

Create `go-server/internal/controlplane/catalog.go`:
```go
package controlplane

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// --- models ---

func (s *Service) CreateModel(ctx context.Context, m Model) (*Model, error) {
	m.ID = uuid.NewString()
	m.Status = "ACTIVE"
	err := s.db.QueryRow(ctx,
		`INSERT INTO models (id, name, description, task, framework, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at, updated_at`,
		m.ID, m.Name, m.Description, m.Task, m.Framework, m.Status).
		Scan(&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}
	return &m, nil
}

func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, description, task, framework, status, created_at, updated_at
		 FROM models ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.Task, &m.Framework,
			&m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) GetModel(ctx context.Context, id string) (*Model, error) {
	var m Model
	err := s.db.QueryRow(ctx,
		`SELECT id, name, description, task, framework, status, created_at, updated_at
		 FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.Description, &m.Task, &m.Framework,
			&m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, err)
	}
	return &m, nil
}

func (s *Service) CreateModelVersion(ctx context.Context, mv ModelVersion) (*ModelVersion, error) {
	mv.ID = uuid.NewString()
	mv.Status = "ACTIVE"
	if mv.Metadata == nil {
		mv.Metadata = map[string]any{}
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO model_versions (id, model_id, version, artifact_uri, metadata_json, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at`,
		mv.ID, mv.ModelID, mv.Version, mv.ArtifactURI, mv.Metadata, mv.Status).
		Scan(&mv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create model version: %w", err)
	}
	return &mv, nil
}

// --- serving templates ---

func (s *Service) CreateTemplate(ctx context.Context, t ServingTemplate) (*ServingTemplate, error) {
	t.ID = uuid.NewString()
	t.Status = "ACTIVE"
	err := s.db.QueryRow(ctx,
		`INSERT INTO serving_templates (id, name, description, runtime, status)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at, updated_at`,
		t.ID, t.Name, t.Description, t.Runtime, t.Status).
		Scan(&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	return &t, nil
}

func (s *Service) ListTemplates(ctx context.Context) ([]ServingTemplate, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, description, runtime, status, created_at, updated_at
		 FROM serving_templates ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()
	out := []ServingTemplate{}
	for rows.Next() {
		var t ServingTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Runtime,
			&t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) GetTemplate(ctx context.Context, id string) (*ServingTemplate, error) {
	var t ServingTemplate
	err := s.db.QueryRow(ctx,
		`SELECT id, name, description, runtime, status, created_at, updated_at
		 FROM serving_templates WHERE id = $1`, id).
		Scan(&t.ID, &t.Name, &t.Description, &t.Runtime,
			&t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get template %s: %w", id, err)
	}
	return &t, nil
}

func (s *Service) CreateTemplateVersion(ctx context.Context, tv TemplateVersion) (*TemplateVersion, error) {
	tv.ID = uuid.NewString()
	if tv.Command == nil {
		tv.Command = []string{}
	}
	if tv.Environment == nil {
		tv.Environment = map[string]string{}
	}
	if tv.ConfigSchema == nil {
		tv.ConfigSchema = map[string]any{}
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO serving_template_versions (id, template_id, version, image, command, environment, config_schema)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING created_at`,
		tv.ID, tv.TemplateID, tv.Version, tv.Image, tv.Command, tv.Environment, tv.ConfigSchema).
		Scan(&tv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create template version: %w", err)
	}
	return &tv, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go-server && go test ./internal/controlplane/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd go-server && git add internal/controlplane && git commit -m "feat(controlplane): model registry + serving template CRUD"
```

---

### Task 7: Deployment domain — entity, state machine, revisions

**Files:**
- Create: `go-server/internal/controlplane/deployment.go` (entity Deployment, DeploymentRevision, Endpoint + query methods)
- Create: `go-server/internal/controlplane/state.go`
- Create: `go-server/internal/controlplane/state_test.go`

**Interfaces:**
- Consumes: `Service` (Task 4)
- Produces: state constants `DeploymentPending/Provisioning/Starting/Ready/Degraded/Stopping/Stopped/Failed`; `CanTransition(current, next string) bool`; methods `CreateDeployment(ctx, d Deployment) (*Deployment, error)`, `GetDeployment(ctx, id string) (*Deployment, error)`, `ListDeployments(ctx, tenantID string) ([]Deployment, error)`, `TransitionDeployment(ctx, id, to string) (*Deployment, error)`, `CreateRevision(ctx, deploymentID string, spec map[string]any, createdBy string) (*DeploymentRevision, error)`, `ListRevisions(ctx, deploymentID string) ([]DeploymentRevision, error)`

- [ ] **Step 1: Write failing state machine test**

Create `go-server/internal/controlplane/state.go` (stub):
```go
package controlplane

const (
	DeploymentPending      = "PENDING"
	DeploymentProvisioning = "PROVISIONING"
	DeploymentStarting     = "STARTING"
	DeploymentReady        = "READY"
	DeploymentDegraded     = "DEGRADED"
	DeploymentStopping     = "STOPPING"
	DeploymentStopped      = "STOPPED"
	DeploymentFailed       = "FAILED"
)

func CanTransition(current, next string) bool { return false }
```

Create `go-server/internal/controlplane/state_test.go`:
```go
package controlplane

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		cur, next string
		want      bool
	}{
		{DeploymentPending, DeploymentProvisioning, true},
		{DeploymentProvisioning, DeploymentStarting, true},
		{DeploymentStarting, DeploymentReady, true},
		{DeploymentStarting, DeploymentDegraded, true},
		{DeploymentReady, DeploymentDegraded, true},
		{DeploymentReady, DeploymentStopping, true},
		{DeploymentDegraded, DeploymentReady, true},
		{DeploymentStopping, DeploymentStopped, true},
		{DeploymentPending, DeploymentFailed, true},
		{DeploymentStopped, DeploymentPending, true},
		{DeploymentReady, DeploymentPending, false},
		{DeploymentFailed, DeploymentReady, false},
		{DeploymentPending, DeploymentReady, false},
		{"BOGUS", DeploymentReady, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.cur, c.next); got != c.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", c.cur, c.next, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/controlplane/ -run TestCanTransition`
Expected: FAIL — `CanTransition(PENDING, PROVISIONING) = false, want true`

- [ ] **Step 3: Implement state.go**

Replace `go-server/internal/controlplane/state.go`:
```go
package controlplane

const (
	DeploymentPending      = "PENDING"
	DeploymentProvisioning = "PROVISIONING"
	DeploymentStarting     = "STARTING"
	DeploymentReady        = "READY"
	DeploymentDegraded     = "DEGRADED"
	DeploymentStopping     = "STOPPING"
	DeploymentStopped      = "STOPPED"
	DeploymentFailed       = "FAILED"
)

// validTransitions maps current state to the set of allowed next states.
var validTransitions = map[string]map[string]bool{
	DeploymentPending:      {DeploymentProvisioning: true, DeploymentFailed: true},
	DeploymentProvisioning: {DeploymentStarting: true, DeploymentFailed: true},
	DeploymentStarting:     {DeploymentReady: true, DeploymentDegraded: true, DeploymentFailed: true},
	DeploymentReady:        {DeploymentDegraded: true, DeploymentStopping: true, DeploymentFailed: true},
	DeploymentDegraded:     {DeploymentReady: true, DeploymentStopping: true, DeploymentFailed: true},
	DeploymentStopping:     {DeploymentStopped: true, DeploymentFailed: true},
	DeploymentStopped:      {DeploymentPending: true},
	DeploymentFailed:       {},
}

// CanTransition reports whether current may move to next.
func CanTransition(current, next string) bool {
	return validTransitions[current][next]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go-server && go test ./internal/controlplane/ -run TestCanTransition`
Expected: PASS

- [ ] **Step 5: Write failing deployment entity test**

Create `go-server/internal/controlplane/deployment.go` (stub with entity only):
```go
package controlplane

import "time"

type Deployment struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id"`
	ModelVersionID    string    `json:"model_version_id"`
	TemplateVersionID string    `json:"template_version_id"`
	Name              string    `json:"name"`
	Region            string    `json:"region"`
	DesiredReplicas   int       `json:"desired_replicas"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type DeploymentRevision struct {
	ID           string         `json:"id"`
	DeploymentID string         `json:"deployment_id"`
	Revision     int            `json:"revision"`
	Spec         map[string]any `json:"spec"`
	CreatedAt    time.Time      `json:"created_at"`
	CreatedBy    string         `json:"created_by"`
}
```

- [ ] **Step 6: Implement deployment queries**

Add to `go-server/internal/controlplane/deployment.go`:
```go
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Deployment struct { /* as Step 5 */ }
type DeploymentRevision struct { /* as Step 5 */ }

func (s *Service) CreateDeployment(ctx context.Context, d Deployment) (*Deployment, error) {
	d.ID = uuid.NewString()
	d.Status = DeploymentPending
	err := s.db.QueryRow(ctx,
		`INSERT INTO deployments (id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.ModelVersionID, d.TemplateVersionID, d.Name, d.Region, d.DesiredReplicas, d.Status).
		Scan(&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	return &d, nil
}

func (s *Service) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, created_at, updated_at
		 FROM deployments WHERE id = $1`, id).
		Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", id, err)
	}
	return &d, nil
}

func (s *Service) ListDeployments(ctx context.Context, tenantID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, model_version_id, template_version_id, name, region, desired_replicas, status, created_at, updated_at
		 FROM deployments WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.TenantID, &d.ModelVersionID, &d.TemplateVersionID, &d.Name,
			&d.Region, &d.DesiredReplicas, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

var ErrInvalidTransition = errors.New("invalid deployment state transition")

// TransitionDeployment moves a deployment to `to`, enforcing the state machine.
func (s *Service) TransitionDeployment(ctx context.Context, id, to string) (*Deployment, error) {
	d, err := s.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanTransition(d.Status, to) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, d.Status, to)
	}
	d.Status = to
	d.UpdatedAt = time.Now().UTC()
	if _, err := s.db.Exec(ctx,
		`UPDATE deployments SET status = $1, updated_at = $2 WHERE id = $3`,
		d.Status, d.UpdatedAt, d.ID); err != nil {
		return nil, fmt.Errorf("update deployment: %w", err)
	}
	return d, nil
}

func (s *Service) CreateRevision(ctx context.Context, deploymentID string, spec map[string]any, createdBy string) (*DeploymentRevision, error) {
	var rev int
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision), 0) + 1 FROM deployment_revisions WHERE deployment_id = $1`,
		deploymentID).Scan(&rev); err != nil {
		return nil, fmt.Errorf("next revision: %w", err)
	}
	r := &DeploymentRevision{
		ID: uuid.NewString(), DeploymentID: deploymentID,
		Revision: rev, Spec: spec, CreatedBy: createdBy,
	}
	if err := s.db.QueryRow(ctx,
		`INSERT INTO deployment_revisions (id, deployment_id, revision, spec_json, created_by)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		r.ID, r.DeploymentID, r.Revision, spec, nullableUUID(createdBy)).Scan(&r.CreatedAt); err != nil {
		return nil, fmt.Errorf("insert revision: %w", err)
	}
	return r, nil
}

func (s *Service) ListRevisions(ctx context.Context, deploymentID string) ([]DeploymentRevision, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, revision, spec_json, created_at, COALESCE(created_by, '')
		 FROM deployment_revisions WHERE deployment_id = $1 ORDER BY revision`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer rows.Close()
	out := []DeploymentRevision{}
	for rows.Next() {
		var r DeploymentRevision
		if err := rows.Scan(&r.ID, &r.DeploymentID, &r.Revision, &r.Spec, &r.CreatedAt, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nullableUUID returns a *string (nil for empty) to satisfy the uuid column.
func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
```

- [ ] **Step 7: Run all controlplane tests**

Run: `cd go-server && go test ./internal/controlplane/...`
Expected: PASS (state machine + entity tests compile; DB methods not hit by unit tests)

- [ ] **Step 8: Commit**

```bash
cd go-server && git add internal/controlplane && git commit -m "feat(controlplane): deployment state machine + revisions"
```

---

### Task 8: Quota CRUD

**Files:**
- Create: `go-server/internal/controlplane/quota.go`
- Create: `go-server/internal/controlplane/quota_test.go`

**Interfaces:**
- Consumes: `Service`, `Quota` (Task 4)
- Produces: type `controlplane.Quota{ID, TenantID, QuotaType, Period string; LimitValue int64}`; methods `UpsertQuota(ctx, q Quota) (*Quota, error)`, `ListQuotas(ctx, tenantID string) ([]Quota, error)`

- [ ] **Step 1: Write failing test**

Create `go-server/internal/controlplane/quota_test.go`:
```go
package controlplane

import (
	"encoding/json"
	"testing"
)

func TestQuotaJSON(t *testing.T) {
	q := Quota{TenantID: "t1", QuotaType: "tokens", LimitValue: 50_000_000, Period: "month"}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Quota
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.QuotaType != "tokens" || out.LimitValue != 50_000_000 || out.Period != "month" {
		t.Errorf("out = %+v", out)
	}
}
```

Create `go-server/internal/controlplane/quota.go` (stub):
```go
package controlplane

type Quota struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	QuotaType  string `json:"quota_type"`
	LimitValue int64  `json:"limit_value"`
	Period     string `json:"period"`
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/controlplane/ -run TestQuotaJSON`
Expected: FAIL — `undefined: Quota`

- [ ] **Step 3: Implement quota.go**

Replace `go-server/internal/controlplane/quota.go`:
```go
package controlplane

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type Quota struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	QuotaType  string `json:"quota_type"`
	LimitValue int64  `json:"limit_value"`
	Period     string `json:"period"`
}

// UpsertQuota creates or updates a tenant quota (unique on tenant/type/period).
func (s *Service) UpsertQuota(ctx context.Context, q Quota) (*Quota, error) {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO tenant_quotas (id, tenant_id, quota_type, limit_value, period)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tenant_id, quota_type, period)
		 DO UPDATE SET limit_value = EXCLUDED.limit_value
		 RETURNING id`,
		q.ID, q.TenantID, q.QuotaType, q.LimitValue, q.Period).Scan(&q.ID)
	if err != nil {
		return nil, fmt.Errorf("upsert quota: %w", err)
	}
	return &q, nil
}

func (s *Service) ListQuotas(ctx context.Context, tenantID string) ([]Quota, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, tenant_id, quota_type, limit_value, period
		 FROM tenant_quotas WHERE tenant_id = $1 ORDER BY quota_type`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list quotas: %w", err)
	}
	defer rows.Close()
	out := []Quota{}
	for rows.Next() {
		var q Quota
		if err := rows.Scan(&q.ID, &q.TenantID, &q.QuotaType, &q.LimitValue, &q.Period); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go-server && go test ./internal/controlplane/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd go-server && git add internal/controlplane && git commit -m "feat(controlplane): tenant quota CRUD"
```

---

### Task 9: HTTP handlers + wiring + E2E (integration)

**Files:**
- Create: `go-server/internal/api/controlplane.go`
- Create: `go-server/internal/api/controlplane_test.go` (integration, gated by env)
- Modify: `go-server/cmd/server/main.go`
- Create: `scripts/m1-demo.sh`

**Interfaces:**
- Consumes: `controlplane.Service` (Task 4/6/7/8), `auth.Service` (Task 5), `auth` middleware (Task 5)
- Produces: `api.NewControlPlaneHandler(cp *controlplane.Service, authSvc *auth.Service, secret []byte) *ControlPlaneHandler`; `(*ControlPlaneHandler).RegisterRoutes(mux *http.ServeMux)` mounting routes dưới; bootstrap helper `seedAdmin(ctx, cp, authSvc) error`

Routes:
- `POST /api/v1/auth/login`
- `POST /api/v1/api-keys` (RequirePermission key.manage)
- `POST /api/v1/tenants` (tenant.manage), `GET /api/v1/tenants` (tenant.read)
- `POST /api/v1/models` (model.write), `GET /api/v1/models` (model.read), `GET /api/v1/models/{id}` (model.read), `POST /api/v1/models/{id}/versions` (model.write)
- `POST /api/v1/templates` (template.write), `GET /api/v1/templates` (template.read), `GET /api/v1/templates/{id}` (template.read), `POST /api/v1/templates/{id}/versions` (template.write)
- `POST /api/v1/deployments` (deployment.write), `GET /api/v1/deployments` (deployment.read), `GET /api/v1/deployments/{id}` (deployment.read), `POST /api/v1/deployments/{id}/start` (deployment.write), `POST /api/v1/deployments/{id}/stop` (deployment.write), `GET /api/v1/deployments/{id}/revisions` (deployment.read)
- `POST /api/v1/quotas` (quota.manage), `GET /api/v1/quotas` (quota.read, own tenant)

- [ ] **Step 1: Write failing handler test (login flow with live DB, gated)**

Create `go-server/internal/api/controlplane_test.go`:
```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
)

// TestLoginE2E runs the full M1 control plane path against a real Postgres.
func TestLoginE2E(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping E2E")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cp := controlplane.NewService(d.Pool())
	secret := []byte("0123456789abcdef")
	authSvc := auth.NewService(cp, secret, time.Hour)

	// bootstrap: tenant + admin user
	tenant, err := cp.CreateTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	hash, _ := auth.HashPassword("admin-pass")
	if _, err := cp.CreateUser(ctx, "admin", "admin@acme.io", hash, auth.RoleTenantAdmin, tenant.ID); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := NewControlPlaneHandler(cp, authSvc, secret)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// login
	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal login: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("empty access_token")
	}
}
```

Create `go-server/internal/api/controlplane.go` (stub):
```go
package api

import (
	"net/http"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
)

type ControlPlaneHandler struct {
	cp     *controlplane.Service
	auth   *auth.Service
	secret []byte
}

func NewControlPlaneHandler(cp *controlplane.Service, authSvc *auth.Service, secret []byte) *ControlPlaneHandler {
	return nil
}

func (h *ControlPlaneHandler) RegisterRoutes(mux *http.ServeMux) {}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go-server && go test ./internal/api/ -run TestLoginE2E`
Expected: FAIL — `NewControlPlaneHandler returned nil` (hoặc build fail vì stub). (Không cần DB để thấy fail — stub trả nil.)

- [ ] **Step 3: Implement controlplane.go**

Create `go-server/internal/api/controlplane.go`:
```go
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
)

// ControlPlaneHandler mounts /api/v1/* routes.
type ControlPlaneHandler struct {
	cp     *controlplane.Service
	auth   *auth.Service
	secret []byte
}

func NewControlPlaneHandler(cp *controlplane.Service, authSvc *auth.Service, secret []byte) *ControlPlaneHandler {
	return &ControlPlaneHandler{cp: cp, auth: authSvc, secret: secret}
}

// RegisterRoutes mounts all control plane routes.
func (h *ControlPlaneHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.handleLogin)

	mux.Handle("POST /api/v1/api-keys", auth.RequirePermission(h.secret, auth.ActionKeyManage)(h.handleCreateAPIKey))

	mux.Handle("POST /api/v1/tenants", auth.RequirePermission(h.secret, auth.ActionTenantManage)(h.handleCreateTenant))
	mux.Handle("GET /api/v1/tenants", auth.RequirePermission(h.secret, auth.ActionTenantRead)(h.handleListTenants))

	mux.Handle("POST /api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelWrite)(h.handleCreateModel))
	mux.Handle("GET /api/v1/models", auth.RequirePermission(h.secret, auth.ActionModelRead)(h.handleListModels))
	mux.Handle("GET /api/v1/models/", auth.RequirePermission(h.secret, auth.ActionModelRead)(h.handleGetModel))
	mux.Handle("POST /api/v1/models/", auth.RequirePermission(h.secret, auth.ActionModelWrite)(h.handleCreateModelVersion))

	mux.Handle("POST /api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateWrite)(h.handleCreateTemplate))
	mux.Handle("GET /api/v1/templates", auth.RequirePermission(h.secret, auth.ActionTemplateRead)(h.handleListTemplates))
	mux.Handle("GET /api/v1/templates/", auth.RequirePermission(h.secret, auth.ActionTemplateRead)(h.handleGetTemplate))
	mux.Handle("POST /api/v1/templates/", auth.RequirePermission(h.secret, auth.ActionTemplateWrite)(h.handleCreateTemplateVersion))

	mux.Handle("POST /api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployWrite)(h.handleCreateDeployment))
	mux.Handle("GET /api/v1/deployments", auth.RequirePermission(h.secret, auth.ActionDeployRead)(h.handleListDeployments))
	mux.Handle("GET /api/v1/deployments/", auth.RequirePermission(h.secret, auth.ActionDeployRead)(h.handleDeploymentByID))
	mux.Handle("POST /api/v1/deployments/", auth.RequirePermission(h.secret, auth.ActionDeployWrite)(h.handleDeploymentAction))

	mux.Handle("POST /api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionQuotaManage)(h.handleUpsertQuota))
	mux.Handle("GET /api/v1/quotas", auth.RequirePermission(h.secret, auth.ActionUsageRead)(h.handleListQuotas))
}

// --- auth ---

func (h *ControlPlaneHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	token, err := h.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token})
}

func (h *ControlPlaneHandler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var req struct {
		Name      string    `json:"name"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	raw, hash := auth.GenerateAPIKey()
	key, err := h.cp.CreateAPIKey(r.Context(), claims.TenantID, req.Name, hash, req.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": key.ID, "tenant_id": key.TenantID, "name": key.Name, "key": raw})
}

// --- tenants ---

func (h *ControlPlaneHandler) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "name required")
		return
	}
	t, err := h.cp.CreateTenant(r.Context(), req.Name)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *ControlPlaneHandler) handleListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.cp.ListTenants(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

// --- models ---

func (h *ControlPlaneHandler) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var m controlplane.Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	m, err := h.cp.CreateModel(r.Context(), m)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (h *ControlPlaneHandler) handleListModels(w http.ResponseWriter, r *http.Request) {
	ms, err := h.cp.ListModels(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (h *ControlPlaneHandler) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "id required")
		return
	}
	m, err := h.cp.GetModel(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "model not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *ControlPlaneHandler) handleCreateModelVersion(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
	id = strings.TrimSuffix(id, "/versions")
	var mv controlplane.ModelVersion
	if err := json.NewDecoder(r.Body).Decode(&mv); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	mv.ModelID = id
	mv, err := h.cp.CreateModelVersion(r.Context(), mv)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mv)
}

// --- templates ---

func (h *ControlPlaneHandler) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var t controlplane.ServingTemplate
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	t, err := h.cp.CreateTemplate(r.Context(), t)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *ControlPlaneHandler) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := h.cp.ListTemplates(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (h *ControlPlaneHandler) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	t, err := h.cp.GetTemplate(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "template not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *ControlPlaneHandler) handleCreateTemplateVersion(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	id = strings.TrimSuffix(id, "/versions")
	var tv controlplane.TemplateVersion
	if err := json.NewDecoder(r.Body).Decode(&tv); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	tv.TemplateID = id
	tv, err := h.cp.CreateTemplateVersion(r.Context(), tv)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, tv)
}

// --- deployments ---

func (h *ControlPlaneHandler) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var d controlplane.Deployment
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	d.TenantID = claims.TenantID // derive tenant from auth, never trust body
	d, err := h.cp.CreateDeployment(r.Context(), d)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (h *ControlPlaneHandler) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	ds, err := h.cp.ListDeployments(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (h *ControlPlaneHandler) handleDeploymentByID(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	id = strings.TrimSuffix(id, "/revisions")
	d, err := h.cp.GetDeployment(r.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/revisions") {
		revs, err := h.cp.ListRevisions(r.Context(), id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, revs)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *ControlPlaneHandler) handleDeploymentAction(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "bad path")
		return
	}
	id, action := parts[0], parts[1]
	d, err := h.cp.GetDeployment(r.Context(), id)
	if err != nil || d.TenantID != claims.TenantID {
		writeAPIError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "deployment not found")
		return
	}
	var to string
	switch action {
	case "start":
		to = controlplane.DeploymentPending
	case "stop":
		to = controlplane.DeploymentStopping
	default:
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "unknown action "+action)
		return
	}
	updated, err := h.cp.TransitionDeployment(r.Context(), id, to)
	if err != nil {
		writeAPIError(w, http.StatusConflict, "RESOURCE_CONFLICT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// --- quotas ---

func (h *ControlPlaneHandler) handleUpsertQuota(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	var q controlplane.Quota
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid body")
		return
	}
	q.TenantID = claims.TenantID
	q, err := h.cp.UpsertQuota(r.Context(), q)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (h *ControlPlaneHandler) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFromContext(r.Context())
	qs, err := h.cp.ListQuotas(r.Context(), claims.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, qs)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
```

- [ ] **Step 4: Run build**

Run: `cd go-server && go build ./...`
Expected: build success (lưu ý: pattern `HandleFunc("POST /api/v1/...")` yêu cầu Go 1.22+; `go 1.25.6` đủ điều kiện)

- [ ] **Step 5: Wire into main.go + bootstrap admin**

Modify `go-server/cmd/server/main.go`:
```go
import (
	...
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Load()
	...
	d, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer d.Pool().Close()
	if err := d.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	cp := controlplane.NewService(d.Pool())
	authSvc := auth.NewService(cp, []byte(cfg.JWTSecret), 15*time.Minute)
	cph := api.NewControlPlaneHandler(cp, authSvc, []byte(cfg.JWTSecret))
	...
	// sau khi tạo mux và h.RegisterRoutes(mux):
	cph.RegisterRoutes(mux)
	mux.Handle("/metrics", observability.MetricsHandler())
}
```

Add bootstrap seeding — create `go-server/cmd/server/seed.go`:
```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/controlplane"
)

// seedAdmin creates the default platform admin + a demo tenant if none exist.
func seedAdmin(ctx context.Context, cp *controlplane.Service) error {
	if os.Getenv("AI_FACTORY_SKIP_SEED") == "1" {
		return nil
	}
	username := envOr("AI_FACTORY_ADMIN_USER", "admin")
	password := envOr("AI_FACTORY_ADMIN_PASSWORD", "admin1234")
	tenantName := envOr("AI_FACTORY_DEMO_TENANT", "acme")

	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("list tenants for seed: %w", err)
	}
	if len(tenants) > 0 {
		return nil
	}
	tenant, err := cp.CreateTenant(ctx, tenantName)
	if err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("seed hash: %w", err)
	}
	if _, err := cp.CreateUser(ctx, username, username+"@localhost", hash, auth.RolePlatformAdmin, tenant.ID); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	log.Printf("seeded tenant=%s admin=%s (password in AI_FACTORY_ADMIN_PASSWORD or default)", tenantName, username)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
```

Call `seedAdmin(ctx, cp)` sau khi tạo `cp`, trước khi start HTTP server.

- [ ] **Step 6: Run E2E test with live DB**

Run:
```bash
docker compose -f deployments/docker-compose.yml up -d postgres
cd go-server && AI_FACTORY_DATABASE_URL='postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable' go test ./internal/api/ -run TestLoginE2E -v
```
Expected: PASS (login trả access_token)

- [ ] **Step 7: Manual smoke test via script**

Create `scripts/m1-demo.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
# M1 demo: login -> create model/template -> create deployment -> start/stop.
BASE="http://localhost:8080"
TOKEN=$(curl -s -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin1234"}' | python -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
AUTH="Authorization: Bearer $TOKEN"

curl -s -X POST "$BASE/api/v1/models" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"qwen3-9b","task":"text-generation","framework":"llama"}' | python -m json.tool
curl -s -X POST "$BASE/api/v1/templates" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"llama-openai","runtime":"llama"}' | python -m json.tool
echo "--- deployments ---"
curl -s "$BASE/api/v1/deployments" -H "$AUTH" | python -m json.tool
echo "--- health ---"
curl -s "$BASE/health"
```

- [ ] **Step 8: Run full test suite**

Run: `cd go-server && go test ./...`
Expected: PASS (unit tests; integration tests skip khi không có `AI_FACTORY_DATABASE_URL`)

- [ ] **Step 9: Commit**

```bash
cd go-server && git add internal/api cmd/server scripts/ && git commit -m "feat(api): control plane HTTP routes + auth wiring + seed"
```

---

---

### Task 10: Docker packaging — Go server container + compose wiring

**Files:**
- Create: `go-server/Dockerfile` (multi-stage; build context = repo root)
- Create: `.dockerignore` (repo root)
- Modify: `deployments/docker-compose.yml` (từ Task 2) — thêm service `server`
- Modify: `deployments/prometheus.yml` (từ Task 2) — target `server:8080`

**Interfaces:**
- Consumes: Task 9 wiring (`config.Load()` đọc env `AI_FACTORY_*`, `/metrics`, `d.Migrate(ctx)` + `seedAdmin` tự chạy khi khởi động), infra compose (Task 2)
- Produces: toàn stack lên bằng 1 lệnh: `docker compose -f deployments/docker-compose.yml up --build -d` → server ở `http://localhost:8080` (auto-migrate + auto-seed admin)

- [ ] **Step 1: Create Dockerfile + .dockerignore**

Create `go-server/Dockerfile`:
```dockerfile
# Build context = repo root (compose truyền context: ..) — cần copy ui/ ngoài go-server/
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go-server/go.mod go-server/go.sum ./
RUN go mod download
COPY go-server/ ./
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY ui/ /app/ui/
EXPOSE 8080
ENTRYPOINT ["/app/server"]
```

Create `.dockerignore` (repo root):
```
.git/
models/
*.gguf
*.exe
*.safetensors
*.bin
.superpowers/
python-worker/
docs/
*.md
```

- [ ] **Step 2: Wire server service into compose + prometheus**

Modify `deployments/docker-compose.yml` — thêm service `server` (sau `prometheus`):
```yaml
  server:
    build:
      context: ..
      dockerfile: go-server/Dockerfile
    ports:
      - "8080:8080"
    environment:
      AI_FACTORY_DATABASE_URL: postgres://ai_factory:ai_factory@postgres:5432/ai_factory?sslmode=disable
      AI_FACTORY_JWT_SECRET: ${AI_FACTORY_JWT_SECRET:-dev-secret-change-me-0123456789}
      AI_FACTORY_LOG_LEVEL: info
    depends_on:
      postgres:
        condition: service_healthy
    extra_hosts:
      - "host.docker.internal:host-gateway"
    command: ["/app/server", "--port", "8080", "--inference-addr", "host.docker.internal:50051", "--ui-dir", "/app/ui", "--workdir", "/app"]
```

Modify `deployments/prometheus.yml` — target chuyển sang service `server`:
```yaml
scrape_configs:
  - job_name: ai-factory
    static_configs:
      - targets: ["server:8080"]
```
(Lưu ý: khi chạy server cục bộ ngoài docker, ghi đè bằng `host.docker.internal:8080`.)

- [ ] **Step 3: Build image**

Run: `docker compose -f deployments/docker-compose.yml build server`
Expected: build success (multi-stage; network download go deps)

- [ ] **Step 4: Boot stack + smoke test**

Run:
```bash
docker compose -f deployments/docker-compose.yml up -d
curl -s http://localhost:8080/health
curl -s http://localhost:8080/metrics | grep serving_requests_total
bash scripts/m1-demo.sh
```
Expected: `/health` OK; `/metrics` ra counters; demo script login + tạo model/template/deployment thành công (server container tự migrate + seed admin `admin`/`admin1234`).

- [ ] **Step 5: Commit**

```bash
git add go-server/Dockerfile .dockerignore deployments/
git commit -m "feat(docker): package Go server + compose wiring (one-command stack)"
```

---

## Self-Review (đã chạy khi viết plan)

1. **Spec coverage** — M1 trong spec §12: PostgreSQL+migrations ✅(Task 2), auth users/tenants/API keys/JWT/RBAC ✅(Task 3–5), CRUD models/templates/deployments + state machine + revisions ✅(Task 6–7), quota schema ✅(Task 8 + migration), /health ✅(có sẵn), /metrics ✅(Task 1), slog ✅(Task 1), docker-compose ✅(Task 2), **Docker packaging Go server ✅(Task 10 — bổ sung theo yêu cầu user)**. Chưa đụng tới: Redis/Kafka/Prometheus scraping config (M3/M2), Python worker container (GPU local) — đúng phạm vi.
2. **Placeholder scan** — không có TBD/TODO; mọi step có code hoặc lệnh chạy cụ thể.
3. **Type consistency** — `auth.RoleAllows(role, action)` dùng trong rbac_test và middleware cùng signature; `controlplane.Service` methods nhất quán qua Task 4→9; `db.Connect/Migrate` khớp Task 2→5→9; `auth.NewService(store Store, ...)` (Task 5) khớp call-site ở Task 9 (`auth.NewService(cp, ...)` — `*controlplane.Service` implement `Store`). Lưu ý: stub `controlplane.go` ở Task 9 Step 1 cố tình trả `nil` để test fail rồi thay bằng implement đầy đủ ở Step 3.

   **Các fix inline trong re-review:**
   - Task 1: bỏ `config.Port` — tránh trùng nguồn với flag `--port` hiện có của `main.go` (giữ Surgical Changes); test env dùng secret ≥16 ký tự để khớp validation.
   - Task 4: `CreateUser` đổi `tx.QueryRow(...).Scan()` → `tx.Exec(...)` (trước kia không có RETURNING sẽ lỗi `ErrNoRows`); thêm integration test `TestTenantUserAPIKeyIntegration` (gated bởi env).
   - Task 5: viết lại hoàn toàn — giới thiệu `auth.Store` interface + fake store để `Login`/`AuthenticateAPIKey` unit-test được thật (test fail→pass đúng nghĩa TDD, thay vì smoke-test cũ không chạm code mới).
