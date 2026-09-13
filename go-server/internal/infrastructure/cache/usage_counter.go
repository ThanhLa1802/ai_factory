package cache

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// usageTTL bounds how long a buffered counter lives. It must comfortably exceed
// the flush interval so a bucket is never lost before it is flushed.
const usageTTL = 48 * time.Hour

const usagePrefix = "usage:"

// UsageBucket is one (tenant, model, day) counter drained from Redis.
type UsageBucket struct {
	TenantID         string
	Model            string
	Day              string // YYYY-MM-DD (UTC)
	PromptTokens     int64
	CompletionTokens int64
	Requests         int64
}

// UsageCounter buffers per-(tenant, model, day) usage in Redis hashes so the
// inference hot path does not write a DB row per turn. A flusher drains them.
type UsageCounter struct {
	rdb *redis.Client
}

// NewUsageCounter builds a counter over a Redis client.
func NewUsageCounter(rdb *redis.Client) *UsageCounter { return &UsageCounter{rdb: rdb} }

func usageKey(tenantID, model, day string) string {
	return usagePrefix + tenantID + ":" + model + ":" + day
}

// Incr adds one request's tokens to the bucket.
func (c *UsageCounter) Incr(ctx context.Context, tenantID, model, day string, promptTokens, completionTokens int) error {
	key := usageKey(tenantID, model, day)
	pipe := c.rdb.TxPipeline()
	pipe.HIncrBy(ctx, key, "prompt", int64(promptTokens))
	pipe.HIncrBy(ctx, key, "completion", int64(completionTokens))
	pipe.HIncrBy(ctx, key, "requests", 1)
	pipe.Expire(ctx, key, usageTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

// Drain returns every buffered bucket and removes its key. A key that changes
// concurrently with the read is skipped this cycle and picked up next time
// (no loss, no double count).
func (c *UsageCounter) Drain(ctx context.Context) ([]UsageBucket, error) {
	keys, err := c.scanKeys(ctx, usagePrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make([]UsageBucket, 0, len(keys))
	for _, key := range keys {
		b, ok, err := c.drainKey(ctx, key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func (c *UsageCounter) drainKey(ctx context.Context, key string) (UsageBucket, bool, error) {
	var vals map[string]string
	err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
		v, err := tx.HGetAll(ctx, key).Result()
		if err != nil {
			return err
		}
		if len(v) == 0 {
			return nil
		}
		vals = v
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, key)
			return nil
		})
		return err
	}, key)
	if errors.Is(err, redis.TxFailedErr) {
		return UsageBucket{}, false, nil // concurrent Incr; try again next cycle
	}
	if err != nil {
		return UsageBucket{}, false, err
	}
	if vals == nil {
		return UsageBucket{}, false, nil
	}
	return bucketFromKey(key, vals), true, nil
}

func (c *UsageCounter) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var (
		cursor uint64
		keys   []string
	)
	for {
		batch, next, err := c.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		if next == 0 {
			return keys, nil
		}
		cursor = next
	}
}

// bucketFromKey reverses usageKey. Format: usage:<tenant>:<model>:<day>.
func bucketFromKey(key string, vals map[string]string) UsageBucket {
	rest := strings.TrimPrefix(key, usagePrefix)
	parts := strings.SplitN(rest, ":", 3)
	b := UsageBucket{}
	if len(parts) == 3 {
		b.TenantID, b.Model, b.Day = parts[0], parts[1], parts[2]
	}
	b.PromptTokens, _ = strconv.ParseInt(vals["prompt"], 10, 64)
	b.CompletionTokens, _ = strconv.ParseInt(vals["completion"], 10, 64)
	b.Requests, _ = strconv.ParseInt(vals["requests"], 10, 64)
	return b
}
