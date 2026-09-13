package cache

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Lock is a Redis distributed lock (SET NX + owner token). It guards one-shot
// operations (e.g. the usage aggregate flush) so only one replica runs them.
type Lock struct {
	rdb *redis.Client
}

// NewLock builds a lock over a Redis client.
func NewLock(rdb *redis.Client) *Lock { return &Lock{rdb: rdb} }

// Acquire tries to take key for ttl. On success it returns a random owner token
// that must be passed to Release; ok is false when another holder owns the key.
func (l *Lock) Acquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	token := uuid.NewString()
	ok, err := l.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	return token, true, nil
}

// Release frees the lock only if token is still the owner. It is a no-op when
// the lock expired or belongs to someone else, so a slow holder can never free
// a lock it no longer owns.
func (l *Lock) Release(ctx context.Context, key, token string) error {
	return l.rdb.Watch(ctx, func(tx *redis.Tx) error {
		val, err := tx.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			return nil // already expired/released
		}
		if err != nil {
			return err
		}
		if val != token {
			return nil // not the owner; leave it alone
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, key)
			return nil
		})
		return err
	}, key)
}
