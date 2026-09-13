package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestKV(t *testing.T) (*KV, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewKV(rdb), s
}

func TestKVSetGetDelete(t *testing.T) {
	ctx := context.Background()
	kv, _ := newTestKV(t)

	if _, ok, err := kv.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("get missing = (%v,%v), want (false,nil)", ok, err)
	}
	if err := kv.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	b, ok, err := kv.Get(ctx, "k")
	if err != nil || !ok || string(b) != "v" {
		t.Fatalf("get = (%q,%v,%v), want v/true/nil", b, ok, err)
	}
	if err := kv.Delete(ctx, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := kv.Get(ctx, "k"); ok {
		t.Fatal("key still present after delete")
	}
}

func TestKVExpires(t *testing.T) {
	ctx := context.Background()
	kv, s := newTestKV(t)

	if err := kv.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	s.FastForward(2 * time.Minute)
	if _, ok, err := kv.Get(ctx, "k"); err != nil || ok {
		t.Fatalf("get after expiry = (%v,%v), want (false,nil)", ok, err)
	}
}
