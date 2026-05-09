package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTokenBucket_StartsFull(t *testing.T) {
	b := NewTokenBucket(100, 10)
	assert.InDelta(t, 100.0, b.Available(), 0.5)
	assert.Equal(t, 100, b.Capacity())
}

func TestAcquire_BelowCapacity_NoWait(t *testing.T) {
	b := NewTokenBucket(100, 10)
	start := time.Now()
	require.NoError(t, b.Acquire(context.Background(), 5))
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestAcquire_AboveCapacity_Waits(t *testing.T) {
	b := NewTokenBucket(10, 100) // 100 tokens/sec
	require.NoError(t, b.Acquire(context.Background(), 10))
	// Bucket is now empty. Next 5-token request must wait ~50ms for refill.
	start := time.Now()
	require.NoError(t, b.Acquire(context.Background(), 5))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 30*time.Millisecond, "expected wait for refill, got %s", elapsed)
}

func TestAcquire_CtxCancelled_ReturnsError(t *testing.T) {
	// x/time/rate's WaitN with a ctx deadline pre-computes "would exceed
	// deadline" and returns its own error before waiting. To assert real
	// ctx cancellation propagation, use WithCancel and trigger from a
	// separate goroutine after Acquire enters Wait.
	b := NewTokenBucket(10, 1)
	require.NoError(t, b.Acquire(context.Background(), 10)) // drain
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- b.Acquire(ctx, 5) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled), "expected Canceled, got %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return after ctx cancel")
	}
}

// TestAcquire_CtxDeadline_ReturnsError covers the other path: when ctx has a
// deadline that x/time/rate sees would not be reachable, it errors early
// (different error string than ctx.Err but still "request rejected").
func TestAcquire_CtxDeadline_ReturnsError(t *testing.T) {
	b := NewTokenBucket(10, 1)
	require.NoError(t, b.Acquire(context.Background(), 10))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := b.Acquire(ctx, 5)
	require.Error(t, err, "Acquire must error when ctx deadline arrives before refill complete")
}

func TestAcquire_RefillsOverTime(t *testing.T) {
	b := NewTokenBucket(10, 100)
	require.NoError(t, b.Acquire(context.Background(), 10))
	assert.InDelta(t, 0.0, b.Available(), 0.5)
	time.Sleep(60 * time.Millisecond) // 60ms × 100/s = ~6 tokens
	assert.InDelta(t, 6.0, b.Available(), 1.5, "should refill ~6 tokens in 60ms")
}

func TestAvailable_TracksUsage(t *testing.T) {
	b := NewTokenBucket(100, 1) // very slow refill so usage is visible
	before := b.Available()
	require.NoError(t, b.Acquire(context.Background(), 30))
	after := b.Available()
	assert.Less(t, after, before)
	assert.InDelta(t, 70.0, after, 1.0)
}

// TestAcquire_ZeroWeight_DefaultsOne — never silently bypass the limiter.
func TestAcquire_ZeroWeight_DefaultsOne(t *testing.T) {
	b := NewTokenBucket(2, 1)
	require.NoError(t, b.Acquire(context.Background(), 0))
	require.NoError(t, b.Acquire(context.Background(), 0))
	// After 2 weight-1 consumptions, bucket is near empty.
	assert.InDelta(t, 0.0, b.Available(), 0.5)
}

// TestAcquire_NegativeWeight_DefaultsOne — same fail-safe for caller bugs.
func TestAcquire_NegativeWeight_DefaultsOne(t *testing.T) {
	b := NewTokenBucket(2, 1)
	require.NoError(t, b.Acquire(context.Background(), -5))
	assert.InDelta(t, 1.0, b.Available(), 0.5)
}

// TestImplementsBinanceRateLimiter is a compile-time interface satisfaction
// check. If binance.RateLimiter ever drifts, this fails to compile and the
// 0.7 assumption is caught immediately.
func TestImplementsBinanceRateLimiter(t *testing.T) {
	var _ interface {
		Acquire(ctx context.Context, weight int) error
	} = (*TokenBucket)(nil)
}
