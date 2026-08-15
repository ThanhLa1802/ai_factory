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
