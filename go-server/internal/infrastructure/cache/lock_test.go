package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLock(t *testing.T) (*Lock, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewLock(rdb), s
}

func TestLockMutualExclusion(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLock(t)

	if _, ok, err := l.Acquire(ctx, "usage:flush", time.Minute); err != nil || !ok {
		t.Fatalf("first acquire = (%v,%v), want true", ok, err)
	}
	if _, ok, err := l.Acquire(ctx, "usage:flush", time.Minute); err != nil || ok {
		t.Fatalf("second acquire while held = (%v,%v), want false", ok, err)
	}
}

func TestLockReleaseOnlyByOwner(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLock(t)

	token, ok, err := l.Acquire(ctx, "k", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire = (%v,%v), want true", ok, err)
	}
	// A different token must not free the lock.
	if err := l.Release(ctx, "k", "someone-else"); err != nil {
		t.Fatalf("release wrong token: %v", err)
	}
	if _, ok, _ := l.Acquire(ctx, "k", time.Minute); ok {
		t.Fatal("lock freed by a foreign token")
	}
	// The owner can free it.
	if err := l.Release(ctx, "k", token); err != nil {
		t.Fatalf("release owner token: %v", err)
	}
	if _, ok, _ := l.Acquire(ctx, "k", time.Minute); !ok {
		t.Fatal("lock not freed after owner release")
	}
}

func TestLockExpires(t *testing.T) {
	ctx := context.Background()
	l, s := newTestLock(t)

	if _, ok, err := l.Acquire(ctx, "k", time.Minute); err != nil || !ok {
		t.Fatalf("acquire = (%v,%v), want true", ok, err)
	}
	s.FastForward(2 * time.Minute)
	if _, ok, err := l.Acquire(ctx, "k", time.Minute); err != nil || !ok {
		t.Fatalf("acquire after expiry = (%v,%v), want true", ok, err)
	}
}
