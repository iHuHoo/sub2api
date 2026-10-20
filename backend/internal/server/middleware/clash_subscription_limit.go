package middleware

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
)

var clashCounter = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`)

type ClashSubscriptionLimiter struct{ client *redis.Client }

func NewClashSubscriptionLimiter(client *redis.Client) *ClashSubscriptionLimiter {
	return &ClashSubscriptionLimiter{client: client}
}
func (l *ClashSubscriptionLimiter) Allow(ctx context.Context, key string, limit int64) (bool, error) {
	if l == nil {
		return false, errors.New("subscription rate limiter unavailable")
	}
	return ClashSubscriptionAllow(ctx, l.client, key, limit)
}

// ClashSubscriptionAllow is fail-closed; callers supply bounded IP/internal-ID keys, never bearer tokens.
func ClashSubscriptionAllow(ctx context.Context, client *redis.Client, key string, limit int64) (bool, error) {
	if client == nil {
		return false, errors.New("subscription rate limiter unavailable")
	}
	n, err := clashCounter.Run(ctx, client, []string{"clash-subscription:" + key}).Int64()
	return err == nil && n <= limit, err
}
