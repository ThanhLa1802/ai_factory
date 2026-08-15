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
