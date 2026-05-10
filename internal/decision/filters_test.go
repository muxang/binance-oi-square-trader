package decision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/storage/postgres/gen"
)

// fakeFilterDeps implements FilterDeps. Tests configure each return channel.
type fakeFilterDeps struct {
	btcDropPct decimal.Decimal
	btcErr     error

	state    gen.CircuitBreakerState
	stateErr error
	tripErr  error
	resetErr error
	tripped  bool
	reset    bool

	activeCount int64
	activeErr   error

	has24h     bool
	has24hErr  error
	last24hSym string
}

func (f *fakeFilterDeps) GetBTCRegime(_ context.Context) (decimal.Decimal, error) {
	return f.btcDropPct, f.btcErr
}
func (f *fakeFilterDeps) GetState(_ context.Context) (gen.CircuitBreakerState, error) {
	return f.state, f.stateErr
}
func (f *fakeFilterDeps) TripBTCHalt(_ context.Context, _, _ time.Time) error {
	f.tripped = true
	return f.tripErr
}
func (f *fakeFilterDeps) ResetHalt(_ context.Context) error {
	f.reset = true
	if f.resetErr == nil {
		f.state.TradingHalted = false
		f.state.HaltReason = pgtype.Text{Valid: false}
		f.state.HaltUntil = pgtype.Timestamptz{Valid: false}
	}
	return f.resetErr
}
func (f *fakeFilterDeps) CountActive(_ context.Context) (int64, error) {
	return f.activeCount, f.activeErr
}
func (f *fakeFilterDeps) HasRecent24hAttempt(_ context.Context, symbol string, _ time.Time) (bool, error) {
	f.last24hSym = symbol
	return f.has24h, f.has24hErr
}

func newDeps() *fakeFilterDeps {
	return &fakeFilterDeps{
		btcDropPct:  decimal.NewFromFloat(0.01), // healthy default
		state:       gen.CircuitBreakerState{ID: 1},
		activeCount: 2,
		has24h:      false,
	}
}

// --- 4 decision paths ---

func TestFilters_AllPass(t *testing.T) {
	deps := newDeps()
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.True(t, r.Passed)
	assert.Empty(t, r.Reason)
	assert.Equal(t, "BTCUSDT", deps.last24hSym, "Step 3 must call HasRecent24hAttempt with correct symbol")
}

func TestFilters_BTCCrash_TripsAndRejects(t *testing.T) {
	deps := newDeps()
	deps.btcDropPct = decimal.NewFromFloat(0.05) // > 0.03 threshold
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonBTCCrash, r.Reason)
	assert.True(t, deps.tripped, "TripBTCHalt must be called on crash")
}

func TestFilters_PositionLimit_Reject(t *testing.T) {
	deps := newDeps()
	deps.activeCount = 5 // = PositionLimit, reject
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonPositionLimit, r.Reason)
	assert.False(t, deps.tripped, "Step 1 passed → no trip")
}

func TestFilters_Recent24h_Reject(t *testing.T) {
	deps := newDeps()
	deps.has24h = true
	r, err := EvaluateGlobalFilters(context.Background(), "ETHUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonRecent24hTrade, r.Reason)
	assert.Equal(t, "ETHUSDT", deps.last24hSym)
}

// --- 3 边界 ---

func TestFilters_AlreadyHalted_Reject(t *testing.T) {
	now := time.Now()
	deps := newDeps()
	deps.state = gen.CircuitBreakerState{
		ID: 1, TradingHalted: true,
		HaltReason: pgtype.Text{String: "btc_5m_crash", Valid: true},
		HaltUntil:  pgtype.Timestamptz{Time: now.Add(20 * time.Minute), Valid: true}, // not yet expired
	}
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", now, deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, "btc_5m_crash", r.Reason, "use halt_reason from state, not bare AlreadyHalted")
	assert.False(t, deps.reset, "halt_until not yet expired → no reset")
}

func TestFilters_HaltExpired_AutoResetAndContinue(t *testing.T) {
	now := time.Now()
	deps := newDeps()
	deps.state = gen.CircuitBreakerState{
		ID: 1, TradingHalted: true,
		HaltReason: pgtype.Text{String: "btc_5m_crash", Valid: true},
		HaltUntil:  pgtype.Timestamptz{Time: now.Add(-5 * time.Minute), Valid: true}, // expired
	}
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", now, deps, FilterConfig{})
	require.NoError(t, err)
	assert.True(t, r.Passed, "expired halt → auto-reset + continue evaluating downstream filters")
	assert.True(t, deps.reset, "ResetHalt must be called when halt_until expired")
}

func TestFilters_BTCRegimeUnavailable_FailSafeReject(t *testing.T) {
	deps := newDeps()
	deps.btcErr = errors.New("redis Nil: btc_5m_change not set")
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonBTCRegimeUnavailable, r.Reason, "data missing = do not enter (SPEC fail-safe)")
}

// --- 2 边界扩展 ---

func TestFilters_BTCDropEdge_StrictGreaterThan(t *testing.T) {
	// drop_pct = 0.03 EXACTLY → does NOT trip (SPEC L206 "≤ 3% 通过", strict >)
	deps := newDeps()
	deps.btcDropPct = decimal.NewFromFloat(0.03)
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.True(t, r.Passed, "drop_pct=0.03 exactly is at boundary, NOT a crash")
	assert.False(t, deps.tripped)

	// drop_pct = 0.030001 → trip
	deps2 := newDeps()
	deps2.btcDropPct = decimal.NewFromFloat(0.030001)
	r2, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps2, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r2.Passed)
	assert.Equal(t, ReasonBTCCrash, r2.Reason)
	assert.True(t, deps2.tripped)
}

func TestFilters_PositionLimitEdge_4VsExactly5(t *testing.T) {
	// 4 active → pass (count < 5)
	deps := newDeps()
	deps.activeCount = 4
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.True(t, r.Passed, "4 active < 5 limit → pass")

	// 5 active → reject (count >= 5)
	deps2 := newDeps()
	deps2.activeCount = 5
	r2, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps2, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r2.Passed)
	assert.Equal(t, ReasonPositionLimit, r2.Reason)
}

// --- defensive: dep-error fail-safe ---

func TestFilters_GetStateError_FailSafe(t *testing.T) {
	deps := newDeps()
	deps.stateErr = errors.New("pg connection lost")
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonCBStateUnavailable, r.Reason)
}

func TestFilters_CountActiveError_FailSafe(t *testing.T) {
	deps := newDeps()
	deps.activeErr = errors.New("pg timeout")
	r, err := EvaluateGlobalFilters(context.Background(), "BTCUSDT", time.Now(), deps, FilterConfig{})
	require.NoError(t, err)
	assert.False(t, r.Passed)
	assert.Equal(t, ReasonCountUnavailable, r.Reason)
}
