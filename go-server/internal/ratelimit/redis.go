package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// concurrencyLease là TTL đặt lên key concurrency khi slot đầu tiên được chiếm
// (0→1). Nếu holder chết giữa chừng (vd crash process, Ctrl+C không unwind defer),
// slot tự nhả sau lease thay vì tăng vĩnh viễn. Tradeoff: nếu một generation dài
// hơn lease, counter sẽ bị reset và có thể vượt concurrency limit; lease được chọn
// dài hơn hẳn mọi generation thực tế nên slot hợp lệ không bao giờ hết hạn giữa chừng.
const concurrencyLease = 10 * time.Minute

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
	if n == 1 {
		// Lần đầu chiếm slot: đặt lease TTL để slot tự nhả nếu holder chết.
		if err := l.rdb.Expire(ctx, key, concurrencyLease).Err(); err != nil {
			_ = l.rdb.Decr(ctx, key).Err() // rollback
			return false, err
		}
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
