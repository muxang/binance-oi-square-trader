// v0.2 gauge audit (Catch 6/7): tests that EvaluateAll unconditionally updates
// the operational gauges so the dashboard doesn't show stale values when an
// early trip preempts the rest of the evaluation chain.
package execution

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/pkg/metrics"
	"trader/internal/storage/postgres/gen"
)

// fakeCBDeps satisfies CircuitBreakerDeps.
type fakeCBDeps struct {
	state            gen.GetCircuitBreakerStateForTripsRow
	stateErr         error
	apiErrCount      int64
	openPositions    []gen.SumOpenUnrealizedSnapshotRow
	openPositionsErr error
	klines           []gen.Kline
	klinesErr        error
	haltsTripped     []gen.TripGenericHaltParams
	rcaInserted      []gen.InsertHaltRCAParams
}

func (f *fakeCBDeps) GetCircuitBreakerStateForTrips(_ context.Context) (gen.GetCircuitBreakerStateForTripsRow, error) {
	return f.state, f.stateErr
}
func (f *fakeCBDeps) TripGenericHalt(_ context.Context, arg gen.TripGenericHaltParams) error {
	f.haltsTripped = append(f.haltsTripped, arg)
	return nil
}
func (f *fakeCBDeps) InsertHaltRCA(_ context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error) {
	f.rcaInserted = append(f.rcaInserted, arg)
	return gen.InsertHaltRCARow{ID: int64(len(f.rcaInserted))}, nil
}
func (f *fakeCBDeps) CountAPIErrorsSince(_ context.Context, _ time.Time) (int64, error) {
	return f.apiErrCount, nil
}
func (f *fakeCBDeps) SumOpenUnrealizedSnapshot(_ context.Context) ([]gen.SumOpenUnrealizedSnapshotRow, error) {
	return f.openPositions, f.openPositionsErr
}
func (f *fakeCBDeps) GetLatestKlines(_ context.Context, _ gen.GetLatestKlinesParams) ([]gen.Kline, error) {
	return f.klines, f.klinesErr
}

// fakeCBBinance satisfies CircuitBreakerBinance.
type fakeCBBinance struct {
	balance    decimal.Decimal
	balanceErr error
}

func (f *fakeCBBinance) GetUSDTBalance(_ context.Context) (decimal.Decimal, error) {
	return f.balance, f.balanceErr
}

func newTestCB(deps *fakeCBDeps, bc *fakeCBBinance) *CircuitBreakerTripper {
	cfg := CircuitBreakerConfig{
		APIErrorRateLimit:     3,
		ConsecutiveLossCount:  8,
		DailyLossHaltPct:      decimal.NewFromFloat(0.08),
		TotalFloatLossHaltPct: decimal.NewFromFloat(0.12),
		BTCCrashHaltPct:       decimal.NewFromFloat(0.03),
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	cb := NewCircuitBreakerTripper(deps, bc, rdb, cfg, zerolog.Nop())
	cb.nowFn = func() time.Time { return time.Unix(1778600000, 0).UTC() }
	return cb
}

// btcBars returns 6 fake 5m bars with given pct drop from oldest open to newest close.
func btcBars(dropPct float64) []gen.Kline {
	oldestOpen := decimal.NewFromInt(80000)
	newestClose := oldestOpen.Mul(decimal.NewFromFloat(1 - dropPct))
	// sqlc gen.GetLatestKlines returns DESC; bars[0]=newest, bars[5]=oldest.
	return []gen.Kline{
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: newestClose, Open: newestClose},
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: newestClose, Open: newestClose},
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: newestClose, Open: newestClose},
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: newestClose, Open: newestClose},
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: newestClose, Open: newestClose},
		{Symbol: "BTCUSDT", Timeframe: "5m", Close: oldestOpen, Open: oldestOpen},
	}
}

// v0.2 gauge audit Bug 2 + 3: UnrealizedPnlTotalUSDT and BTC30MinDropPct must
// be .Set() unconditionally at the top of EvaluateAll, not only inside their
// own trip helper.
func TestEvaluateAll_AlwaysUpdatesUnrealizedAndBTCGauges(t *testing.T) {
	// Seed gauges with stale values from a prior tick.
	metrics.UnrealizedPnlTotalUSDT.Set(-999)
	metrics.BTC30MinDropPct.Set(0.99)

	deps := &fakeCBDeps{
		state: gen.GetCircuitBreakerStateForTripsRow{
			DailyPnl:          decimal.NewFromInt(-1),
			ConsecutiveLosses: 0,
		},
		apiErrCount:   0,
		openPositions: nil,    // 0 open trades → totalUnrealized = 0
		klines:        btcBars(0.001), // 0.1% drop, below 3% threshold
	}
	bc := &fakeCBBinance{balanceErr: assert.AnError} // balance fetch fails

	cb := newTestCB(deps, bc)
	tripped := cb.EvaluateAll(context.Background())
	assert.False(t, tripped, "no trip with healthy state + balance fail")

	// Both gauges must reflect fresh values even though balance fetch failed
	// (which previously skipped the trip-internal .Set() calls).
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.UnrealizedPnlTotalUSDT), 1e-9,
		"UnrealizedPnlTotalUSDT must reflect current (0 positions) — not stale -999")
	assert.InDelta(t, 0.001, testutil.ToFloat64(metrics.BTC30MinDropPct), 1e-6,
		"BTC30MinDropPct must reflect current (0.1%%) — not stale 0.99")
}

// v0.2 gauge audit Bug 4 + 5: DailyPnlUSDT + ConsecutiveLossesGauge must
// update BEFORE the halted-return so dashboard shows live values during halt.
func TestEvaluateAll_UpdatesGaugesEvenWhenHalted(t *testing.T) {
	metrics.DailyPnlUSDT.Set(-999)
	metrics.ConsecutiveLossesGauge.Set(99)

	deps := &fakeCBDeps{state: gen.GetCircuitBreakerStateForTripsRow{
		TradingHalted:     true, // halt → early return
		HaltReason:        pgtype.Text{String: "manual", Valid: true},
		DailyPnl:          decimal.NewFromInt(-50),
		ConsecutiveLosses: 3,
	}}
	bc := &fakeCBBinance{}
	cb := newTestCB(deps, bc)

	tripped := cb.EvaluateAll(context.Background())
	assert.False(t, tripped, "already halted → no new trip")
	assert.InDelta(t, -50.0, testutil.ToFloat64(metrics.DailyPnlUSDT), 1e-9,
		"DailyPnl must be updated before halt-return (fresh -50, not stale -999)")
	assert.InDelta(t, 3.0, testutil.ToFloat64(metrics.ConsecutiveLossesGauge), 1e-9,
		"ConsecutiveLosses must be updated before halt-return (fresh 3, not stale 99)")
}

// v0.2 Step 4 联动: TripAPIErrorRate fires at the documented threshold
// (≥3 api_errors in last 1min). Verifies Round 6 + Step 4 wiring chain:
// CountAPIErrorsSince returns 3 → trip fires → halt set → rca recorded.
func TestEvaluateAll_TripAPIErrorRate_FiresAt3Errors(t *testing.T) {
	deps := &fakeCBDeps{
		state:       gen.GetCircuitBreakerStateForTripsRow{},
		apiErrCount: 3, // exactly threshold
		klines:      btcBars(0.001),
	}
	bc := &fakeCBBinance{balance: decimal.NewFromInt(1000)}
	cb := newTestCB(deps, bc)

	tripped := cb.EvaluateAll(context.Background())
	assert.True(t, tripped, "api_error count = threshold → trip")
	require.Len(t, deps.haltsTripped, 1, "halt tripped once")
	assert.Equal(t, "circuit_breaker_api_error", deps.haltsTripped[0].HaltReason.String)
	require.Len(t, deps.rcaInserted, 1, "halt_rca written")
	assert.Equal(t, "circuit_breaker_api_error", deps.rcaInserted[0].HaltType)
}

// v0.2 Step 4 联动: below threshold = no trip. Boundary check (2 errors should
// NOT trip even though we're 1 below).
func TestEvaluateAll_TripAPIErrorRate_NoTripBelow3(t *testing.T) {
	deps := &fakeCBDeps{
		state:       gen.GetCircuitBreakerStateForTripsRow{},
		apiErrCount: 2,
		klines:      btcBars(0.001),
	}
	bc := &fakeCBBinance{balance: decimal.NewFromInt(1000)}
	cb := newTestCB(deps, bc)
	tripped := cb.EvaluateAll(context.Background())
	assert.False(t, tripped, "api_error count = 2 < threshold 3 → no trip")
	assert.Empty(t, deps.haltsTripped, "no halt")
}

// Bug 2 verified at trip time: even when an EARLY trip fires (api_error),
// updateUnrealizedGauge + updateBTCDropGauge already ran at the top so the
// gauges reflect current state when mu opens the dashboard mid-incident.
func TestEvaluateAll_EarlyTripStillUpdatesGauges(t *testing.T) {
	metrics.UnrealizedPnlTotalUSDT.Set(-777)
	metrics.BTC30MinDropPct.Set(0.88)

	deps := &fakeCBDeps{
		state:         gen.GetCircuitBreakerStateForTripsRow{},
		apiErrCount:   5, // > 3 threshold → trip api_error
		openPositions: nil,
		klines:        btcBars(0.005), // 0.5% drop
	}
	bc := &fakeCBBinance{balance: decimal.NewFromInt(1000)}
	cb := newTestCB(deps, bc)

	tripped := cb.EvaluateAll(context.Background())
	assert.True(t, tripped, "api_error count > threshold → trip")
	assert.Len(t, deps.haltsTripped, 1)
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.UnrealizedPnlTotalUSDT), 1e-9,
		"UnrealizedPnlTotalUSDT updated before api_error trip preempted")
	assert.InDelta(t, 0.005, testutil.ToFloat64(metrics.BTC30MinDropPct), 1e-6,
		"BTC30MinDropPct updated before api_error trip preempted")
}
