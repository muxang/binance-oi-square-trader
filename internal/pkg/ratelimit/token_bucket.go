package ratelimit

import (
	"context"

	"golang.org/x/time/rate"
)

// TokenBucket wraps golang.org/x/time/rate.Limiter to satisfy the
// binance.RateLimiter interface defined in 0.7. Capacity is the max tokens
// held (burst); RefillPerSecond is the steady-state refill rate.
//
// For Binance USDⓈ-M Futures: 2400 weight/min, used at 80% (ARCHITECTURE §9
// "实际只用 80%, 留 20% 余量") → capacity=1920, refillPerSecond=32 (1920/60).
type TokenBucket struct {
	limiter  *rate.Limiter
	capacity int
}

// NewTokenBucket constructs a bucket starting full (capacity tokens available).
func NewTokenBucket(capacity int, refillPerSecond int) *TokenBucket {
	return &TokenBucket{
		limiter:  rate.NewLimiter(rate.Limit(refillPerSecond), capacity),
		capacity: capacity,
	}
}

// Acquire blocks until `weight` tokens are available, ctx is cancelled, or
// `weight` exceeds capacity. Implements binance.RateLimiter.
//
// A weight of 0 is treated as 1 — we never silently bypass the limiter.
func (t *TokenBucket) Acquire(ctx context.Context, weight int) error {
	if weight <= 0 {
		weight = 1
	}
	return t.limiter.WaitN(ctx, weight)
}

// Available returns the (approximate) current token count. Suitable for a
// Prometheus gauge `binance_api_weight_used = capacity - Available()`.
func (t *TokenBucket) Available() float64 {
	return t.limiter.Tokens()
}

// Capacity returns the bucket's max burst size.
func (t *TokenBucket) Capacity() int {
	return t.capacity
}
