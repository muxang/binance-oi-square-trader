package signal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfgpkg "trader/internal/config"
)

// fakeDataAccess implements SignalDataAccess. Tests configure each return
// channel; insertedRecord captures the last write for assertion.
type fakeDataAccess struct {
	oiSeries      []decimal.Decimal
	hashtagSeries []decimal.Decimal
	closeNow      decimal.Decimal
	closePrior    decimal.Decimal

	getOIErr      error
	getHashtagErr error
	getKlinesErr  error
	insertErr     error

	inserted SignalRecord
	written  bool
}

func (f *fakeDataAccess) GetOIHistory(_ context.Context, _ string, _ int) ([]decimal.Decimal, error) {
	return f.oiSeries, f.getOIErr
}
func (f *fakeDataAccess) GetHashtagHistory(_ context.Context, _ string, _ int) ([]decimal.Decimal, error) {
	return f.hashtagSeries, f.getHashtagErr
}
func (f *fakeDataAccess) GetKlinesCloseNowAndPrior(_ context.Context, _ string, _ time.Duration) (decimal.Decimal, decimal.Decimal, error) {
	return f.closeNow, f.closePrior, f.getKlinesErr
}
func (f *fakeDataAccess) InsertSignal(_ context.Context, rec SignalRecord) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = rec
	f.written = true
	return nil
}

// burstOI = Round 2 anchored fixture (all 4 conds met → OISurge.Triggered=true)
func burstOI() []decimal.Decimal {
	return dseries(100, 95, 90, 95, 100, 105, 110, 115, 120, 130)
}

// burstHashtag = Round 3 standard-mode burst (97 samples → hot=true)
func burstHashtag() []decimal.Decimal {
	baseline := linearGrowth(100, 1.25, 90)
	last := baseline[len(baseline)-1]
	burst := []decimal.Decimal{
		last.Add(d(20)), last.Add(d(50)), last.Add(d(100)),
		last.Add(d(200)), last.Add(d(400)), last.Add(d(700)), last.Add(d(1100)),
	}
	return concatDS(baseline, burst)
}

// flatHashtag = enough samples but flat → SquareHot.Hot=false (low_ratio)
func flatHashtag() []decimal.Decimal {
	return linearGrowth(100, 0.1, 97) // n=97, 24h, ratio≈1
}

// --- 4 decision paths ---

func TestEvaluate_OITriggeredAndHot_EntersFull(t *testing.T) {
	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.True(t, deps.written)
	assert.Equal(t, "entered_full", rec.Decision)
	assert.True(t, rec.OITriggered)
	assert.True(t, rec.SquareHot)
	assert.Empty(t, rec.RejectionReason)
}

func TestEvaluate_OITriggeredButNotHot_EntersHalf(t *testing.T) {
	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: flatHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.Equal(t, "entered_half", rec.Decision)
	assert.True(t, rec.OITriggered)
	assert.False(t, rec.SquareHot)
	assert.Empty(t, rec.RejectionReason, "entered_half is NOT rejected, no reason field")
}

func TestEvaluate_OINotTriggered_Rejected_WithReason(t *testing.T) {
	// Linear OI = no growth → cond 1 fails → low_growth_from_min
	flatOI := dseries(100, 99, 100, 101, 100.5, 101, 101.5, 102, 102.5, 103)
	deps := &fakeDataAccess{
		oiSeries: flatOI, hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.Equal(t, "rejected", rec.Decision)
	assert.False(t, rec.OITriggered)
	assert.Equal(t, "low_growth_from_min", rec.RejectionReason)
}

func TestEvaluate_InsufficientOIData_RejectedWithReason(t *testing.T) {
	// Only 3 OI samples → insufficient_oi_history
	deps := &fakeDataAccess{
		oiSeries: dseries(100, 101, 102), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.Equal(t, "rejected", rec.Decision)
	assert.Contains(t, rec.RejectionReason, "insufficient_oi_history")
}

// --- 3 error propagation ---

func TestEvaluate_GetOIError_BubblesUp(t *testing.T) {
	deps := &fakeDataAccess{getOIErr: errors.New("pg connection lost")}
	_, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get oi history")
	assert.False(t, deps.written, "no insert when read fails")
}

func TestEvaluate_GetHashtagError_BubblesUp(t *testing.T) {
	deps := &fakeDataAccess{
		oiSeries:      burstOI(),
		getHashtagErr: errors.New("pg timeout"),
	}
	_, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get hashtag history")
	assert.False(t, deps.written)
}

func TestEvaluate_InsertSignalError_BubblesUp(t *testing.T) {
	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
		insertErr: errors.New("pg disk full"),
	}
	_, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert signal")
}

// --- 2 JSONB schema sanity (Phase 3 决策引擎读 oi_data / square_data 必须可恢复) ---

func TestSignalRecord_OIData_MarshalsToJSONBSchema(t *testing.T) {
	r := OISurgeResult{
		Triggered: true, GrowthFromMin: d(0.4444), RecentGrowth: d(0.30),
		GrowingPeriods: 5, RecentPeriodsCount: 5, PriceMovedUp: true,
	}
	b, err := MarshalOIDataJSON(r)
	require.NoError(t, err)
	// Round-trip: marshal → unmarshal → assert equivalent.
	var got OISurgeResult
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, r.Triggered, got.Triggered)
	assert.True(t, r.GrowthFromMin.Equal(got.GrowthFromMin))
	assert.True(t, r.RecentGrowth.Equal(got.RecentGrowth))
	assert.Equal(t, r.GrowingPeriods, got.GrowingPeriods)
	// Verify snake_case keys present (Phase 3 grep-able schema).
	assert.Contains(t, string(b), `"triggered"`)
	assert.Contains(t, string(b), `"growth_from_min"`)
	assert.Contains(t, string(b), `"recent_growth"`)
}

func TestSignalRecord_SquareData_MarshalsToJSONBSchema(t *testing.T) {
	r := SquareHotResult{
		Hot: true, Mode: ModeStandard, Ratio: d(2.5), Acceleration: d(0.83),
		SampleCount: 97, DataSpanHours: d(24),
	}
	b, err := MarshalSquareDataJSON(r)
	require.NoError(t, err)
	var got SquareHotResult
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, r.Hot, got.Hot)
	assert.Equal(t, r.Mode, got.Mode)
	assert.True(t, r.Ratio.Equal(got.Ratio))
	assert.Contains(t, string(b), `"hot"`)
	assert.Contains(t, string(b), `"mode":"standard"`)
	assert.Contains(t, string(b), `"data_span_hours"`)
}

// Round 2.y signal_engine refactor: hot-reloadable OI_GROWTH_FROM_MIN_PCT —
// runtime override tightens the OI surge threshold without restart.
//
// burstOI() has growth_from_min = (130-90)/90 = 44.4%. With the default
// internal 5% threshold and admin override 0.06 both let it through.
// We instead override to 50% (impossibly tight) to prove the override
// path actually drives the algo: trigger flips to rejected.
func TestEvaluate_RuntimeOiGrowthOverride_TightensThreshold(t *testing.T) {
	cfgpkg.Set(&cfgpkg.Runtime{OiGrowthFromMinPct: decimal.NewFromFloat(0.50)})
	defer cfgpkg.Set(nil)

	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.Equal(t, "rejected", rec.Decision, "runtime 50%% threshold should reject burst (~44%% growth)")
	assert.Equal(t, "low_growth_from_min", rec.RejectionReason)
}

// Round 2.y signal_engine refactor: hot-reloadable SQUARE_HOT_MULTIPLIER —
// runtime override tightens the Square ratio threshold across all 3 modes.
//
// burstHashtag()'s recent/baseline ratio is well above the default 2.0/2.5
// thresholds. Setting an impossibly tight 1000.0 multiplier flips hot=false
// while OI still triggers → decision = entered_half.
func TestEvaluate_RuntimeSquareMultiplierOverride_BlocksHot(t *testing.T) {
	cfgpkg.Set(&cfgpkg.Runtime{SquareHotMultiplier: decimal.NewFromFloat(1000.0)})
	defer cfgpkg.Set(nil)

	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.True(t, rec.OITriggered, "OI still triggers")
	assert.False(t, rec.SquareHot, "runtime 10.0 multiplier should suppress Square hot")
	assert.Equal(t, "entered_half", rec.Decision)
}

// Zero runtime → algorithm cfg / internal defaults still apply (no clobber).
func TestEvaluate_RuntimeZero_FallsThrough(t *testing.T) {
	cfgpkg.Set(&cfgpkg.Runtime{}) // all zero fields
	defer cfgpkg.Set(nil)

	deps := &fakeDataAccess{
		oiSeries: burstOI(), hashtagSeries: burstHashtag(),
		closeNow: d(51000), closePrior: d(50000),
	}
	rec, err := Evaluate(context.Background(), "BTCUSDT", time.Now(), deps, CompoundConfig{})
	require.NoError(t, err)
	assert.Equal(t, "entered_full", rec.Decision, "zero runtime → internal defaults reach algo")
}
