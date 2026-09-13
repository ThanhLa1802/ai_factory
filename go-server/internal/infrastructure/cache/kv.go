package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// KV is a small byte-oriented Redis cache used for cache-aside. A miss is
// reported as (nil, false, nil); callers treat errors as "no cache" and fall
// back to the source of truth.
type KV struct {
	rdb *redis.Client
}

// NewKV builds a byte cache over a Redis client.
func NewKV(rdb *redis.Client) *KV { return &KV{rdb: rdb} }

// Get returns the cached value and whether it was present.
func (k *KV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := k.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Set stores val under key for ttl.
func (k *KV) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return k.rdb.Set(ctx, key, val, ttl).Err()
}

// Delete removes key.
func (k *KV) Delete(ctx context.Context, key string) error {
	return k.rdb.Del(ctx, key).Err()
}
