# Phase 1: Composition Root + DI + Config + Logging — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the production-style foundation of the modular-monolith rearchitecture — a hand-written DI container (`pkg/di`), a viper config loader, a zap-backed logger, and an `internal/app` composition root — and shrink `cmd/server/main.go` to a thin entry point. **No behaviour change**: every existing endpoint, SSE stream, agentic loop, and test must keep working.

**Architecture:** This is Phase 1 of the design `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md` (§4, §6.3, §6.4, §8). Wiring currently living in `cmd/server/main.go` (middleware, route registration, seed, worker startup, graceful shutdown) moves into `internal/app` (`registry.go` registers providers; `app.go` owns the runtime lifecycle). Dependencies are built lazily by `pkg/di.Container`. This phase deliberately does **not** touch Gin, GORM, or the service-module split — those are Phases 2–4.

**Tech Stack:** Go 1.25.7, `github.com/spf13/viper` (new), `go.uber.org/zap` + `go.uber.org/zap/exp/zapslog` + `gopkg.in/natefinch/lumberjack.v2` (new), existing net/http + pgx + goose.

## Global Constraints

- Module `github.com/ai-factory/go-server`, Go 1.25.7.
- **No behaviour change.** All existing tests must pass unmodified except where explicitly moved (HTTP middleware tests move with the code from `cmd/server` to `internal/app`).
- `Config` keeps its **flat fields** (`DatabaseURL`, `JWTSecret`, `LogLevel`, `KafkaAddr`, `RedisAddr`, `RateLimitRPM`, `RateLimitConcurrency`) so no consumer churns in this phase.
- **Backward-compatible env vars**: every `AI_FACTORY_*` variable keeps working and still overrides the YAML file.
- JWT secret validation is preserved verbatim: env set-but-empty → hard error; unset → dev default; `< 16` chars → error.
- Kafka stays **optional at boot** (warn + in-memory fallback if unreachable) — the chat path must boot without Kafka.
- Middleware order is unchanged: CORS → trace → logging → metrics (outermost to innermost, as today in `main.go`).
- `statusRecorder` must keep implementing `http.Flusher` (SSE depends on it — regression test `TestStatusRecorderImplementsFlusher` moves with it).
- Security: never log API keys, passwords, or raw prompts.
- Every task is TDD: write the failing test, watch it fail, implement, watch it pass, `go vet ./...`, commit.

## File Structure

| File | Responsibility |
|---|---|
| `go-server/pkg/di/container.go` | Generic lazy DI container: register (transient/singleton), resolve, circular detection, lifecycle |
| `go-server/pkg/di/errors.go` | `ProviderError`, `CircularDependencyError` |
| `go-server/pkg/di/lifecycle.go` | `Lifecycle`/`Shutdowner`/`Closer` interfaces |
| `go-server/pkg/di/container_test.go` | resolve/transient/singleton/circular/provider-error/lifecycle/duplicate tests |
| `go-server/configs/config.yaml` | Default configuration (viper) |
| `go-server/internal/config/config.go` | viper loader; keeps flat `Config` fields + JWT validation |
| `go-server/internal/config/config_test.go` | defaults / env override / file load / env-over-file / JWT validation |
| `go-server/internal/observability/log.go` | `SetupLogger` builds a zap core, bridges to `slog` via `zapslog`, optional lumberjack file sink |
| `go-server/internal/observability/log_test.go` | level parsing + JSON output assertions |
| `go-server/internal/app/options.go` | `Options` (flags not yet in Config: inference addr, workdir, max-concurrent, ui-dir) |
| `go-server/internal/app/registry.go` | `RegisterAll(container, cfg, opts)` — 5 provider groups |
| `go-server/internal/app/app.go` | `App`, `NewAppFromContainer`, `Run`, middleware + route setup, graceful shutdown |
| `go-server/internal/app/app_test.go` | middleware tests moved from `cmd/server/main_test.go` |
| `go-server/internal/app/seeder.go` | `seedAdmin`, `seedDemo` moved from `cmd/server/seed.go` |
| `go-server/cmd/server/main.go` | thin: flags → config.Load → SetupLogger → NewContainer → RegisterAll → NewAppFromContainer → Run |
| `go-server/cmd/server/main_test.go` | deleted (tests moved to `internal/app`) |
| `go-server/go.mod` | new deps (viper, zap, zapslog, lumberjack) |
| `CLAUDE.md`, `docs/TRACKING.md` | record Phase 1 + spec link |

---

### Task 1: `pkg/di` DI container

**Files:**
- Create: `go-server/pkg/di/errors.go`
- Create: `go-server/pkg/di/lifecycle.go`
- Create: `go-server/pkg/di/container.go`
- Create: `go-server/pkg/di/container_test.go`

**Interfaces:**
- Produces (used by Task 4):
  - `type ProviderFunc func(c *Container) (any, error)`
  - `func NewContainer() *Container`
  - `func (c *Container) Register(name string, provider ProviderFunc) error`
  - `func (c *Container) RegisterSingleton(name string, provider ProviderFunc) error`
  - `func (c *Container) Resolve(name string) (any, error)`
  - `func (c *Container) MustResolve(name string) any`
  - `func (c *Container) Shutdown(ctx context.Context) error`
  - `func (c *Container) Close() error`
  - `type Lifecycle interface { Shutdown(ctx context.Context) error }`
  - `type Closer interface { Close() error }`

- [ ] **Step 1: Write the failing tests**

Create `go-server/pkg/di/container_test.go`:

```go
package di

import (
	"context"
	"errors"
	"testing"
)

func TestResolveUnknown(t *testing.T) {
	c := NewContainer()
	if _, err := c.Resolve("nope"); err == nil {
		t.Fatal("Resolve(unknown) = nil, want error")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	c := NewContainer()
	if err := c.RegisterSingleton("x", func(*Container) (any, error) { return 1, nil }); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.RegisterSingleton("x", func(*Container) (any, error) { return 2, nil }); err == nil {
		t.Fatal("duplicate register = nil, want error")
	}
}

func TestTransientResolvesEachTime(t *testing.T) {
	c := NewContainer()
	calls := 0
	_ = c.Register("t", func(*Container) (any, error) { calls++; return calls, nil })
	_, _ = c.Resolve("t")
	v, _ := c.Resolve("t")
	if calls != 2 || v.(int) != 2 {
		t.Fatalf("calls=%d v=%v, want 2 resolutions", calls, v)
	}
}

func TestSingletonResolvesOnce(t *testing.T) {
	c := NewContainer()
	calls := 0
	_ = c.RegisterSingleton("s", func(*Container) (any, error) { calls++; return struct{}{}, nil })
	a, _ := c.Resolve("s")
	b, _ := c.Resolve("s")
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if a == nil || b == nil {
		t.Fatal("singleton returned nil")
	}
}

func TestProviderErrorWrapped(t *testing.T) {
	c := NewContainer()
	boom := errors.New("boom")
	_ = c.RegisterSingleton("bad", func(*Container) (any, error) { return nil, boom })
	_, err := c.Resolve("bad")
	if !errors.Is(err, boom) {
		t.Fatalf("Resolve err = %v, want to wrap boom", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *ProviderError", err)
	}
}

func TestCircularDependency(t *testing.T) {
	c := NewContainer()
	_ = c.RegisterSingleton("a", func(cc *Container) (any, error) { return cc.Resolve("b") })
	_ = c.RegisterSingleton("b", func(cc *Container) (any, error) { return cc.Resolve("a") })
	_, err := c.Resolve("a")
	var ce *CircularDependencyError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CircularDependencyError", err)
	}
}

type fakeComponent struct{ shutdowns int }

func (f *fakeComponent) Shutdown(ctx context.Context) error { f.shutdowns++; return nil }

func TestShutdownCallsLifecycleReverseOrder(t *testing.T) {
	c := NewContainer()
	first := &fakeComponent{}
	second := &fakeComponent{}
	var order []string
	_ = c.RegisterSingleton("first", func(*Container) (any, error) { order = append(order, "first"); return first, nil })
	_ = c.RegisterSingleton("second", func(*Container) (any, error) { order = append(order, "second"); return second, nil })
	_, _ = c.Resolve("first")
	_, _ = c.Resolve("second")
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if first.shutdowns != 1 || second.shutdowns != 1 {
		t.Fatalf("shutdowns = %d/%d, want 1/1", first.shutdowns, second.shutdowns)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("build order = %v", order)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd go-server && go test ./pkg/di/ -v`
Expected: FAIL — `no Go files in .../pkg/di`.

- [ ] **Step 3: Implement errors + lifecycle**

Create `go-server/pkg/di/errors.go`:

```go
package di

import "fmt"

// ProviderError wraps a provider failure and names the provider.
type ProviderError struct {
	Name string
	Err  error
}

func (e *ProviderError) Error() string { return fmt.Sprintf("di: provider %q: %v", e.Name, e.Err) }
func (e *ProviderError) Unwrap() error { return e.Err }

// CircularDependencyError is returned when resolving a provider re-enters itself.
type CircularDependencyError struct{ Name string }

func (e *CircularDependencyError) Error() string {
	return fmt.Sprintf("di: circular dependency at %q", e.Name)
}
```

Create `go-server/pkg/di/lifecycle.go`:

```go
package di

import "context"

// Lifecycle is implemented by components that need cleanup. Shutdown is called
// once per resolved singleton, in reverse resolution order.
type Lifecycle interface {
	Shutdown(ctx context.Context) error
}

// Closer is a lighter lifecycle for components exposing Close() error.
type Closer interface {
	Close() error
}
```

- [ ] **Step 4: Implement the container**

Create `go-server/pkg/di/container.go`:

```go
// Package di is a minimal, dependency-free lazy DI container with lifecycle
// support and circular-dependency detection. It is intentionally name-based:
// the composition root (internal/app) is the only place that knows strings.
package di

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ProviderFunc builds a component. It may Resolve its own dependencies.
type ProviderFunc func(c *Container) (any, error)

type entry struct {
	name      string
	provider  ProviderFunc
	singleton bool
	instance  any
	resolved  bool
	resolving bool
}

// Container holds registered providers. It is safe for a single composition
// thread (the app boot); it is not designed for concurrent Resolve of the same
// key.
type Container struct {
	mu      sync.Mutex
	entries map[string]*entry
	order   []string // resolution order of singletons (for reverse Shutdown)
}

func NewContainer() *Container {
	return &Container{entries: map[string]*entry{}}
}

// Register adds a transient provider (a fresh instance per Resolve).
func (c *Container) Register(name string, provider ProviderFunc) error {
	return c.register(name, provider, false)
}

// RegisterSingleton adds a lazy singleton provider (built once, then cached).
func (c *Container) RegisterSingleton(name string, provider ProviderFunc) error {
	return c.register(name, provider, true)
}

func (c *Container) register(name string, provider ProviderFunc, singleton bool) error {
	if provider == nil {
		return fmt.Errorf("di: nil provider for %q", name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[name]; ok {
		return fmt.Errorf("di: %q already registered", name)
	}
	c.entries[name] = &entry{name: name, provider: provider, singleton: singleton}
	return nil
}

// Resolve builds (or returns the cached instance of) the named component.
func (c *Container) Resolve(name string) (any, error) {
	c.mu.Lock()
	e, ok := c.entries[name]
	if !ok {
		c.mu.Unlock()
		return nil, &ProviderError{Name: name, Err: errors.New("not registered")}
	}
	if e.resolving {
		c.mu.Unlock()
		return nil, &CircularDependencyError{Name: name}
	}
	if e.singleton && e.resolved {
		inst := e.instance
		c.mu.Unlock()
		return inst, nil
	}
	e.resolving = true
	c.mu.Unlock()

	inst, err := e.provider(c)

	c.mu.Lock()
	e.resolving = false
	if err != nil {
		c.mu.Unlock()
		return nil, &ProviderError{Name: name, Err: err}
	}
	if e.singleton {
		e.instance = inst
		e.resolved = true
		c.order = append(c.order, name)
	}
	c.mu.Unlock()
	return inst, nil
}

// MustResolve is Resolve but panics on error — for use inside providers wired
// by the composition root, where a failure is a programming error.
func (c *Container) MustResolve(name string) any {
	v, err := c.Resolve(name)
	if err != nil {
		panic(err)
	}
	return v
}

// Shutdown calls Shutdown on every resolved singleton implementing Lifecycle,
// in reverse resolution order. Errors are joined.
func (c *Container) Shutdown(ctx context.Context) error {
	return c.closeWith(func(inst any) error {
		if l, ok := inst.(Lifecycle); ok {
			return l.Shutdown(ctx)
		}
		return nil
	})
}

// Close calls Close on every resolved singleton implementing Closer, in reverse
// resolution order.
func (c *Container) Close() error {
	return c.closeWith(func(inst any) error {
		if cl, ok := inst.(Closer); ok {
			return cl.Close()
		}
		return nil
	})
}

func (c *Container) closeWith(fn func(inst any) error) error {
	c.mu.Lock()
	order := append([]string(nil), c.order...)
	insts := make([]any, 0, len(order))
	for _, name := range order {
		if e := c.entries[name]; e != nil {
			insts = append(insts, e.instance)
		}
	}
	c.mu.Unlock()

	var errs []error
	for i := len(insts) - 1; i >= 0; i-- {
		if err := fn(insts[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 5: Run to verify they pass**

Run: `cd go-server && go test ./pkg/di/ -v`
Expected: PASS (7 tests).

- [ ] **Step 6: Vet + commit**

Run: `cd go-server && go vet ./...`
Expected: clean.

```bash
git add go-server/pkg/di
git commit -m "feat(di): lazy DI container with lifecycle + circular detection"
```

---

### Task 2: viper config loader + `configs/config.yaml`

**Files:**
- Create: `go-server/configs/config.yaml`
- Modify: `go-server/internal/config/config.go`
- Modify: `go-server/internal/config/config_test.go`
- Modify: `go-server/go.mod` (add `github.com/spf13/viper`)

**Interfaces:**
- Produces (used by Task 4 + `cmd/server/main.go`):
  - `func Load(path string) (*Config, error)` — `path == ""` means "no file, defaults + env only".
  - `Config` keeps its existing flat fields unchanged.

- [ ] **Step 1: Update the tests for the new signature**

Modify `go-server/internal/config/config_test.go`:

- Change every `Load()` call to `Load("")`.
- Keep `TestLoadDefaults`, `TestLoadJWTSecretSetButEmpty`, `TestLoadFromEnv`, `TestRateLimitDefaults`, `TestRateLimitEnvOverride` as-is otherwise (they assert the flat fields).
- Add these new tests:

```go
func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("log_level: warn\nkafka_addr: filehost:9092\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	unsetenv(t, "AI_FACTORY_JWT_SECRET")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "")
	t.Setenv("AI_FACTORY_KAFKA_ADDR", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(file): %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn (from file)", cfg.LogLevel)
	}
	if cfg.KafkaAddr != "filehost:9092" {
		t.Errorf("KafkaAddr = %q, want filehost:9092 (from file)", cfg.KafkaAddr)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("log_level: warn\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	unsetenv(t, "AI_FACTORY_JWT_SECRET")
	t.Setenv("AI_FACTORY_LOG_LEVEL", "debug")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(file+env): %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (env must win over file)", cfg.LogLevel)
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	if _, err := Load("does-not-exist.yaml"); err == nil {
		t.Fatal("Load(missing file) = nil, want error")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd go-server && go test ./internal/config/ -v`
Expected: FAIL — `Load` takes no arguments / config file assertions fail.

- [ ] **Step 3: Add the dependency**

Run: `cd go-server && go get github.com/spf13/viper@latest`

- [ ] **Step 4: Implement the viper loader**

Rewrite `go-server/internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds runtime configuration. Values come from configs/config.yaml,
// overridden by AI_FACTORY_* environment variables. Fields stay flat so this
// phase does not churn any consumer.
type Config struct {
	DatabaseURL          string
	JWTSecret            string
	LogLevel             string
	KafkaAddr            string
	RedisAddr            string
	RateLimitRPM         int
	RateLimitConcurrency int
}

// Load reads configuration from an optional YAML file plus AI_FACTORY_* env
// overrides. path == "" loads defaults + env only.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetDefault("database_url", "postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable")
	v.SetDefault("jwt_secret", "dev-secret-change-me")
	v.SetDefault("log_level", "info")
	v.SetDefault("kafka_addr", "localhost:9092")
	v.SetDefault("redis_addr", "localhost:6379")
	v.SetDefault("rate_limit_rpm", 60)
	v.SetDefault("rate_limit_concurrency", 4)

	v.SetEnvPrefix("AI_FACTORY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	// JWT secret keeps its strict, set-but-empty semantics (viper treats an
	// empty env var as unset, which is not what we want for a secret).
	secret, ok := os.LookupEnv("AI_FACTORY_JWT_SECRET")
	if ok && secret == "" {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET is set but empty")
	}
	if secret == "" {
		secret = v.GetString("jwt_secret")
	}
	if len(secret) < 16 {
		return nil, fmt.Errorf("AI_FACTORY_JWT_SECRET must be at least 16 characters")
	}

	return &Config{
		DatabaseURL:          v.GetString("database_url"),
		JWTSecret:            secret,
		LogLevel:             v.GetString("log_level"),
		KafkaAddr:            v.GetString("kafka_addr"),
		RedisAddr:            v.GetString("redis_addr"),
		RateLimitRPM:         v.GetInt("rate_limit_rpm"),
		RateLimitConcurrency: v.GetInt("rate_limit_concurrency"),
	}, nil
}
```

- [ ] **Step 5: Create the default config file**

Create `go-server/configs/config.yaml`:

```yaml
# AI Factory — default configuration. AI_FACTORY_* env vars override these.
database_url: "postgres://ai_factory:ai_factory@localhost:5432/ai_factory?sslmode=disable"
jwt_secret: "dev-secret-change-me"
log_level: "info"
kafka_addr: "localhost:9092"
redis_addr: "localhost:6379"
rate_limit_rpm: 60
rate_limit_concurrency: 4
```

- [ ] **Step 6: Run to verify they pass**

Run: `cd go-server && go test ./internal/config/ -v`
Expected: PASS (all tests, including the 3 new ones).

- [ ] **Step 7: Vet + commit**

Run: `cd go-server && go mod tidy && go vet ./...`

```bash
git add go-server/configs go-server/internal/config go-server/go.mod go-server/go.sum
git commit -m "feat(config): viper loader + configs/config.yaml with env override"
```

---

### Task 3: zap + lumberjack logger (slog bridge)

**Files:**
- Modify: `go-server/internal/observability/log.go`
- Create: `go-server/internal/observability/log_test.go`
- Modify: `go-server/go.mod`

**Interfaces:**
- Preserves the existing signature `func SetupLogger(level string) *slog.Logger`, so **no call site changes** (13 non-test files keep calling `slog.Info`).
- Adds `func newZapCore(w io.Writer, level string) zapcore.Core` (unexported, testable).

- [ ] **Step 1: Write the failing test**

Create `go-server/internal/observability/log_test.go`:

```go
package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestZapCoreEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	core := newZapCore(&buf, "debug")
	logger := slog.New(newSlogHandler(core))
	logger.Info("hello", "tenant", "t1")

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v (raw=%q)", err, buf.String())
	}
	if got["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", got["msg"])
	}
	if got["tenant"] != "t1" {
		t.Errorf("tenant = %v, want t1", got["tenant"])
	}
}

func TestZapCoreRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	core := newZapCore(&buf, "error")
	logger := slog.New(newSlogHandler(core))
	logger.Info("dropped")
	if strings.Contains(buf.String(), "dropped") {
		t.Fatalf("info logged at error level: %q", buf.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd go-server && go test ./internal/observability/ -run TestZapCore -v`
Expected: FAIL — `undefined: newZapCore` / `undefined: newSlogHandler`.

- [ ] **Step 3: Add the dependencies**

Run:
```bash
cd go-server
go get go.uber.org/zap@latest
go get go.uber.org/zap/exp/zapslog@latest
go get gopkg.in/natefinch/lumberjack.v2@latest
```

> If module `go.uber.org/zap/exp/zapslog` cannot be fetched, fall back to a hand-written `slog.Handler` that writes `zapcore`-encoded entries; do **not** change call sites. Record the fallback in the commit message.

- [ ] **Step 4: Implement the logger**

Rewrite `go-server/internal/observability/log.go`:

```go
package observability

import (
	"io"
	"log/slog"
	"os"

	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// SetupLogger configures the process-wide slog logger, backed by a zap core
// (JSON), and returns it. The slog API is kept at call sites; zap is the
// implementation. If AI_FACTORY_LOG_FILE is set, logs are also written there
// with rotation.
func SetupLogger(level string) *slog.Logger {
	w := io.Writer(os.Stdout)
	if file := os.Getenv("AI_FACTORY_LOG_FILE"); file != "" {
		w = io.MultiWriter(os.Stdout, &lumberjack.Logger{
			Filename:   file,
			MaxSize:    100, // MB
			MaxBackups: 3,
			MaxAge:     7, // days
			Compress:   true,
		})
	}
	logger := slog.New(newSlogHandler(newZapCore(w, level)))
	slog.SetDefault(logger)
	return logger
}

// newSlogHandler bridges a zap core into the slog API.
func newSlogHandler(core zapcore.Core) slog.Handler {
	return zapslog.NewHandler(core, nil)
}

// newZapCore builds a JSON zap core at the given level.
func newZapCore(w io.Writer, level string) zapcore.Core {
	enc := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	})
	return zapcore.NewCore(enc, zapcore.AddSync(w), zapLevel(level))
}

func zapLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
```

- [ ] **Step 5: Run to verify they pass**

Run: `cd go-server && go test ./internal/observability/ -v`
Expected: PASS (existing + 2 new tests).

- [ ] **Step 6: Vet + commit**

Run: `cd go-server && go vet ./... && go test ./... 2>&1 | Select-String -Pattern "FAIL"` (expect no FAIL)

```bash
git add go-server/internal/observability go-server/go.mod go-server/go.sum
git commit -m "feat(observability): zap+lumberjack logger bridged into slog"
```

---

### Task 4: `internal/app` composition root

**Files:**
- Create: `go-server/internal/app/options.go`
- Create: `go-server/internal/app/registry.go`
- Create: `go-server/internal/app/app.go`
- Create: `go-server/internal/app/seeder.go`
- Create: `go-server/internal/app/app_test.go`

**Interfaces:**
- Consumes: `pkg/di` (Task 1), `config.Config` (Task 2), `observability.SetupLogger` (Task 3), all existing internal packages.
- Produces (used by Task 5):
  - `type Options struct { InferenceAddr string; WorkDir string; MaxConcurrent int; UIDir string }`
  - `func RegisterAll(c *di.Container, cfg *config.Config, opts Options) error`
  - `func NewAppFromContainer(c *di.Container, cfg *config.Config) (*App, error)`
  - `func (a *App) Run() error`

- [ ] **Step 1: Move the middleware tests**

Move `cmd/server/main_test.go` → `go-server/internal/app/app_test.go`, change `package main` to `package app`. Keep both tests (`TestMetricsMiddleware`, `TestStatusRecorderImplementsFlusher`) and their imports unchanged. Delete `cmd/server/main_test.go`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd go-server && go test ./internal/app/ -v`
Expected: FAIL — `undefined: metricsMiddleware` / `undefined: statusRecorder`.

- [ ] **Step 3: Create options**

Create `go-server/internal/app/options.go`:

```go
package app

// Options are process settings that still live on flags rather than Config.
type Options struct {
	InferenceAddr string
	WorkDir       string
	MaxConcurrent int
	UIDir         string
}
```

- [ ] **Step 4: Create the seeder (moved verbatim from `cmd/server/seed.go`)**

Create `go-server/internal/app/seeder.go` with the exact bodies of `seedAdmin`, `envOr`, `seedDemo` from `cmd/server/seed.go`, changing only the package clause to `package app`. Keep `package main`'s imports as needed.

- [ ] **Step 5: Create the registry**

Create `go-server/internal/app/registry.go`. Register providers in dependency order. Key registration (mirror the current `main.go` construction exactly):

```go
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/ai-factory/go-server/internal/agent"
	"github.com/ai-factory/go-server/internal/api"
	"github.com/ai-factory/go-server/internal/auth"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/db"
	"github.com/ai-factory/go-server/internal/events"
	"github.com/ai-factory/go-server/internal/inference"
	"github.com/ai-factory/go-server/internal/ratelimit"
	"github.com/ai-factory/go-server/internal/runtime"
	"github.com/ai-factory/go-server/internal/session"
	"github.com/ai-factory/go-server/pkg/di"
	"github.com/redis/go-redis/v9"
)

// busBundle groups the event bus with whether it is the real Kafka bus.
type busBundle struct {
	Producer events.Producer
	Consumer events.Consumer
	Kafka    bool
}

// RegisterAll wires every component into the container in dependency order:
// infrastructure → factories → services → handlers → workers.
func RegisterAll(c *di.Container, cfg *config.Config, opts Options) error {
	// 1. Infrastructure
	if err := c.RegisterSingleton("logger", func(*di.Container) (any, error) { return slog.Default(), nil }); err != nil {
		return err
	}
	if err := c.RegisterSingleton("db", func(*di.Container) (any, error) {
		ctx := context.Background()
		d, err := db.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		if err := d.Migrate(ctx); err != nil {
			return nil, err
		}
		return d, nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("redis", func(*di.Container) (any, error) {
		return redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("limiter", func(cc *di.Container) (any, error) {
		return ratelimit.NewRedisLimiter(cc.MustResolve("redis").(*redis.Client)), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("bus", func(*di.Container) (any, error) {
		if kafkaBus, err := events.NewKafkaEventBus(cfg.KafkaAddr); err == nil {
			return &busBundle{Producer: kafkaBus, Consumer: kafkaBus, Kafka: true}, nil
		}
		slog.Warn("kafka unreachable; deployment worker disabled", "addr", cfg.KafkaAddr)
		mem := events.NewMemoryEventBus()
		return &busBundle{Producer: mem, Consumer: mem, Kafka: false}, nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("inference.client", func(*di.Container) (any, error) {
		return inference.NewClient(opts.InferenceAddr)
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("batch.scheduler", func(cc *di.Container) (any, error) {
		ic := cc.MustResolve("inference.client").(*inference.Client)
		s := inference.NewBatchScheduler(ic)
		if opts.MaxConcurrent > 1 {
			s.SetMaxBatchSize(opts.MaxConcurrent)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("tool.executor", func(*di.Container) (any, error) {
		return agent.NewLocalToolExecutor(opts.WorkDir), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("agent.loop", func(cc *di.Container) (any, error) {
		return agent.NewLoop(cc.MustResolve("batch.scheduler").(*inference.BatchScheduler),
			cc.MustResolve("tool.executor").(*agent.LocalToolExecutor)), nil
	}); err != nil {
		return err
	}

	// 2. Services
	if err := c.RegisterSingleton("controlplane", func(cc *di.Container) (any, error) {
		return controlplane.NewService(cc.MustResolve("db").(*db.DB).Pool()), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("auth", func(cc *di.Container) (any, error) {
		return auth.NewService(cc.MustResolve("controlplane").(*controlplane.Service),
			[]byte(cfg.JWTSecret), 8*time.Hour), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("session.manager", func(cc *di.Container) (any, error) {
		pool := cc.MustResolve("db").(*db.DB).Pool()
		return session.NewManagerWithStore(session.NewPGStore(pool)), nil
	}); err != nil {
		return err
	}

	// 3. Workers (started conditionally by App.Run)
	if err := c.RegisterSingleton("deployment.worker", func(cc *di.Container) (any, error) {
		b := cc.MustResolve("bus").(*busBundle)
		return runtime.NewWorker(
			cc.MustResolve("controlplane").(*controlplane.Service),
			runtime.NewWorkerAdapter(opts.InferenceAddr),
			runtime.NewMockComputeProvider(),
			b.Producer, b.Consumer, slog.Default(),
		), nil
	}); err != nil {
		return err
	}

	// 4. HTTP handlers + routes
	if err := c.RegisterSingleton("http.handler", func(cc *di.Container) (any, error) {
		b := cc.MustResolve("bus").(*busBundle)
		return api.NewHandler(
			cc.MustResolve("session.manager").(*session.Manager),
			cc.MustResolve("agent.loop").(*agent.Loop),
			resolveUIDir(opts.UIDir),
			cc.MustResolve("auth").(*auth.Service),
			[]byte(cfg.JWTSecret),
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("limiter").(*ratelimit.RedisLimiter),
			cfg.RateLimitRPM, cfg.RateLimitConcurrency,
		), nil
	}); err != nil {
		return err
	}
	if err := c.RegisterSingleton("http.controlplane", func(cc *di.Container) (any, error) {
		b := cc.MustResolve("bus").(*busBundle)
		return api.NewControlPlaneHandler(
			cc.MustResolve("controlplane").(*controlplane.Service),
			cc.MustResolve("auth").(*auth.Service),
			[]byte(cfg.JWTSecret), b.Producer,
		), nil
	}); err != nil {
		return err
	}
	return nil
}

// resolveUIDir reproduces the current auto-detect (ui/ then ../ui).
func resolveUIDir(explicit string) string { /* moved logic from main.go */ }
```

> **Careful:** verify the exact constructor signatures against the current `main.go` before finalizing (`api.NewHandler`, `api.NewControlPlaneHandler`, `ratelimit.NewRedisLimiter` return type, `agent.NewLoop`, `session.NewManagerWithStore`). If `ratelimit.NewRedisLimiter` returns an interface rather than `*RedisLimiter`, use that interface type in the assertion.

- [ ] **Step 6: Create the app lifecycle**

Create `go-server/internal/app/app.go`:

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/controlplane"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/internal/runtime"
	"github.com/ai-factory/go-server/pkg/di"
)

// App owns the runtime lifecycle: seed → start worker → serve HTTP → shutdown.
type App struct {
	cfg       *config.Config
	container *di.Container
	log       *slog.Logger
	port      int
}

// NewAppFromContainer force-resolves the critical singletons so a bad config
// fails fast at boot rather than at first request.
func NewAppFromContainer(c *di.Container, cfg *config.Config, port int) (*App, error) {
	for _, name := range []string{
		"db", "controlplane", "auth", "bus", "session.manager",
		"agent.loop", "http.handler", "http.controlplane",
	} {
		if _, err := c.Resolve(name); err != nil {
			return nil, err
		}
	}
	return &App{cfg: cfg, container: c, log: slog.Default(), port: port}, nil
}

// Run seeds, starts the worker (if Kafka is up), serves HTTP, and blocks until
// SIGINT/SIGTERM, then shuts the container down.
func (a *App) Run() error {
	ctx := context.Background()
	cp := a.container.MustResolve("controlplane").(*controlplane.Service)
	if err := seedAdmin(ctx, cp); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := seedDemo(ctx, cp); err != nil {
		a.log.Warn("seed demo deployment", "err", err)
	}

	if b := a.container.MustResolve("bus").(*busBundle); b.Kafka {
		w := a.container.MustResolve("deployment.worker").(*runtime.Worker)
		if err := w.Run(ctx); err != nil {
			return fmt.Errorf("deployment worker: %w", err)
		}
		a.log.Info("deployment worker started (async deploy)")
	}

	mux := http.NewServeMux()
	a.container.MustResolve("http.handler").(interface{ RegisterRoutes(*http.ServeMux) }).RegisterRoutes(mux)
	a.container.MustResolve("http.controlplane").(interface{ RegisterRoutes(*http.ServeMux) }).RegisterRoutes(mux)
	mux.Handle("/metrics", observability.MetricsHandler())

	handler := corsMiddleware(traceMiddleware(loggingMiddleware(metricsMiddleware(mux))))
	server := &http.Server{Addr: fmt.Sprintf(":%d", a.port), Handler: handler}

	shutdownDone := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		a.log.Info("shutting down")
		server.Close()
		_ = a.container.Shutdown(context.Background())
		_ = a.container.Close()
		close(shutdownDone)
	}()

	a.log.Info("server listening", "addr", fmt.Sprintf(":%d", a.port))
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	<-shutdownDone
	return nil
}
```

Then **move** `loggingMiddleware`, `corsMiddleware`, `traceMiddleware`, `metricsMiddleware`, and `statusRecorder` (plus its `SetRouteLabels`/`Flush` methods) verbatim from `cmd/server/main.go` into `app.go`.

- [ ] **Step 7: Run to verify it passes**

Run: `cd go-server && go test ./internal/app/ -v`
Expected: PASS (`TestMetricsMiddleware`, `TestStatusRecorderImplementsFlusher`).

- [ ] **Step 8: Build the whole module**

Run: `cd go-server && go build ./...`
Expected: succeeds.

- [ ] **Step 9: Vet + commit**

Run: `cd go-server && go vet ./...`

```bash
git add go-server/internal/app go-server/cmd/server
git commit -m "feat(app): composition root + registry + lifecycle (wiring out of main)"
```

---

### Task 5: Thin `cmd/server/main.go`

**Files:**
- Modify: `go-server/cmd/server/main.go`
- Delete: `go-server/cmd/server/seed.go` (moved to Task 4)
- Delete: `go-server/cmd/server/main_test.go` (moved to Task 4)

**Interfaces:**
- Consumes: `config.Load` (Task 2), `observability.SetupLogger` (Task 3), `app.RegisterAll` / `app.NewAppFromContainer` / `app.Run` (Task 4).

- [ ] **Step 1: Rewrite main.go**

Replace `go-server/cmd/server/main.go` with:

```go
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/ai-factory/go-server/internal/app"
	"github.com/ai-factory/go-server/internal/config"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/ai-factory/go-server/pkg/di"
)

func main() {
	var (
		httpPort      = flag.Int("port", 8080, "HTTP server port")
		configPath    = flag.String("config", "configs/config.yaml", "Path to YAML config (empty to skip)")
		inferenceAddr = flag.String("inference-addr", "localhost:50051", "Python inference worker gRPC address")
		workDir       = flag.String("workdir", ".", "Working directory for tool execution")
		maxConcurrent = flag.Int("max-concurrent", 1, "Max concurrent inference requests")
		uiDir         = flag.String("ui-dir", "", "Directory with standalone UI HTML (default: auto-detect)")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	observability.SetupLogger(cfg.LogLevel)
	slog.Info("AI Factory Server starting", "http_port", *httpPort, "inference_addr", *inferenceAddr)

	container := di.NewContainer()
	if err := app.RegisterAll(container, cfg, app.Options{
		InferenceAddr: *inferenceAddr,
		WorkDir:       *workDir,
		MaxConcurrent: *maxConcurrent,
		UIDir:         *uiDir,
	}); err != nil {
		slog.Error("register", "err", err)
		os.Exit(1)
	}
	application, err := app.NewAppFromContainer(container, cfg, *httpPort)
	if err != nil {
		slog.Error("boot", "err", err)
		os.Exit(1)
	}
	if err := application.Run(); err != nil {
		slog.Error("run", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
```

- [ ] **Step 2: Build**

Run: `cd go-server && go build ./...`
Expected: succeeds.

- [ ] **Step 3: Full test suite**

Run: `cd go-server && go test ./...`
Expected: all PASS (integration tests may SKIP without `AI_FACTORY_DATABASE_URL`).

- [ ] **Step 4: Boot smoke test (requires Postgres up)**

Start Postgres, then run the server and hit health:

```bash
docker compose -f deployments/docker-compose.yml up -d postgres redis
cd go-server && go run ./cmd/server/ &
sleep 3
curl -s http://localhost:8080/health
```

Expected: boot logs show JSON (zap shape), `/health` responds, no panic. Stop the server afterward.

- [ ] **Step 5: Chat smoke test (requires Python worker up)**

With the Python worker running, login and send one message to confirm SSE still works end-to-end:

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{"model":"qwen3.5-9b","messages":[{"role":"user","content":"Hello"}],"stream":true}' | head -c 300
```

Expected: SSE `data:` chunks stream (no "streaming not supported").

- [ ] **Step 6: Commit**

```bash
git add go-server/cmd/server
git commit -m "refactor(server): thin main.go bootstrapping via internal/app"
```

---

### Task 6: Documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/TRACKING.md`

- [ ] **Step 1: Update `docs/TRACKING.md`**

Add a section under "✅ ..." blocks:

```markdown
## 🏗️ Kiến trúc lại theo production blueprint (Phase 1 ✅)

Trạng thái: **Phase 1 xong** — nền tảng composition root + DI + config + logging.

- Spec: `docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`
- Plan: `docs/superpowers/plans/2026-09-11-phase1-composition-root-di.md`
- Đã thêm: `pkg/di` (lazy DI + lifecycle), viper config (`configs/config.yaml`), zap logger (bridge slog), `internal/app` composition root; `cmd/server/main.go` teo lại.
- Kế tiếp: Phase 2 — GORM + gormigrate + repository/interface.
```

And add a line to the update log table.

- [ ] **Step 2: Update `CLAUDE.md`**

In the "Architecture" or "Key Decisions" area, add a note that the Go server is migrating to a modular-monolith layout per the spec, with Phase 1 (composition root + DI + viper + zap) done, and link the spec/plan.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/TRACKING.md
git commit -m "docs: record Phase 1 of modular-monolith rearchitecture"
```

---

## Self-Review Checkpoints

Before declaring Phase 1 done, verify:

- [ ] `go build ./...` and `go test ./...` are clean in `go-server`.
- [ ] `cmd/server/main.go` contains **no** business wiring — only flags, config, logger, container, app.
- [ ] `internal/app/app.go` holds the middleware chain in the exact original order.
- [ ] `statusRecorder` still implements `http.Flusher` (its test passes).
- [ ] Every `AI_FACTORY_*` env var still overrides `configs/config.yaml`.
- [ ] JWT validation semantics unchanged (set-but-empty errors; default for dev; length ≥ 16).
- [ ] Kafka-down boot still works (warn + in-memory bus; server serves).
- [ ] No new behaviour in endpoints — this phase is a pure refactor plus config/log backend swap.
