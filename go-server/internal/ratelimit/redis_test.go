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

func TestAcquireLeaseSelfReleases(t *testing.T) {
	ctx := context.Background()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	l := NewRedisLimiter(rdb)

	// First holder takes the only slot and arms the lease TTL.
	ok, err := l.Acquire(ctx, "c", 1)
	if err != nil || !ok {
		t.Fatalf("first acquire = (%v,%v), want true", ok, err)
	}
	// While the slot is held, a second acquire must fail.
	if ok, _ := l.Acquire(ctx, "c", 1); ok {
		t.Fatal("acquire while held = true, want false")
	}
	// Advance past the lease: a crashed holder's slot self-releases.
	s.FastForward(concurrencyLease + time.Minute)
	ok, err = l.Acquire(ctx, "c", 1)
	if err != nil || !ok {
		t.Fatalf("acquire after lease expiry = (%v,%v), want true", ok, err)
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
