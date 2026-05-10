package signal

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// d builds decimal from float for test fixtures (not for prod money math).
func d(v float64) decimal.Decimal { return decimal.NewFromFloat(v) }

// dseries builds a []decimal.Decimal from float literals.
func dseries(vs ...float64) []decimal.Decimal {
	out := make([]decimal.Decimal, len(vs))
	for i, v := range vs {
		out[i] = d(v)
	}
	return out
}

func TestOISurge_AllConditionsMet_Triggered(t *testing.T) {
	// Dip then sustained rise: min=98 @ idx 2, current=110, growthFromMin=12.2%
	// recent 6 (idx 4..9)=[100,101,103,105,107,110], recentGrowth=10%, growing=5
	oi := dseries(100, 99, 98, 99, 100, 101, 103, 105, 107, 110)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.True(t, r.Triggered)
	assert.Empty(t, r.FailedReason)
	assert.True(t, r.GrowthFromMin.GreaterThan(d(0.05)))
	assert.True(t, r.RecentGrowth.GreaterThan(d(0.03)))
	assert.GreaterOrEqual(t, r.GrowingPeriods, 3)
	assert.True(t, r.PriceMovedUp)
}

func TestOISurge_GrowthBelow5Pct_FailReasonLowGrowth(t *testing.T) {
	// min=99 (idx 1), current=103, growthFromMin=4.04% < 5%
	oi := dseries(100, 99, 100, 101, 100.5, 101, 101.5, 102, 102.5, 103)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.Equal(t, "low_growth_from_min", r.FailedReason)
}

func TestOISurge_RecentGrowthBelow3Pct_FailReasonRecentFlat(t *testing.T) {
	// min=50 (idx 0), current=100, growthFromMin=100% (cond 1 ✓)
	// recent 6 (idx 4..9)=[100,99,100,101,100,100], recentGrowth=0% < 3% (cond 2 ✗)
	oi := dseries(50, 51, 52, 53, 100, 99, 100, 101, 100, 100)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.Equal(t, "recent_flat", r.FailedReason)
}

func TestOISurge_NotEnoughGrowingPeriods_FailReasonNoTrend(t *testing.T) {
	// One step up then flat: cond 1 (50→60 = 20%) + cond 2 (50→60 = 20%) ✓,
	// but growingPeriods = 1 (only one 50→60 transition) < 3 (cond 3 ✗)
	oi := dseries(50, 50, 50, 50, 50, 60, 60, 60, 60, 60)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.Equal(t, "no_uptrend", r.FailedReason)
	assert.Equal(t, 1, r.GrowingPeriods, "only 50→60 transition counts in last 5 pairs")
}

func TestOISurge_PriceFlat_FailReasonPriceNotMovedUp(t *testing.T) {
	// All 3 OI conds met (reuse Test 1 fixture), but closeNow == closeMinusOneHour
	oi := dseries(100, 99, 98, 99, 100, 101, 103, 105, 107, 110)
	r, err := OISurge(oi, d(50000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.Equal(t, "price_not_moved_up", r.FailedReason)
	assert.False(t, r.PriceMovedUp)
}

func TestOISurge_InsufficientOIHistory_Skip(t *testing.T) {
	oi := dseries(100, 101, 102) // 3 < RecentPeriods default 6
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.True(t, strings.HasPrefix(r.FailedReason, "insufficient_oi_history"),
		"want 'insufficient_oi_history' prefix, got %q", r.FailedReason)
}

func TestOISurge_ZeroMinOI_NoCrash(t *testing.T) {
	// min == 0 in lookback window — must short-circuit, no division panic
	oi := dseries(0, 50, 60, 70, 80, 90, 100, 110, 120, 130)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.False(t, r.Triggered)
	assert.Equal(t, "zero_min_oi", r.FailedReason)
}

func TestOISurge_ZeroCloseMinusOneHour_NoCrash(t *testing.T) {
	// closeMinusOneHour == 0 is a degenerate but valid input (no klines 60min ago).
	// Cond 4 is comparison only (no division), so closeNow=51000 > 0 → priceMovedUp=true.
	oi := dseries(100, 99, 98, 99, 100, 101, 103, 105, 107, 110)
	r, err := OISurge(oi, d(51000), decimal.Zero, OISurgeConfig{})
	require.NoError(t, err)
	assert.True(t, r.PriceMovedUp, "any positive close > 0 base, no division")
	assert.True(t, r.Triggered, "all 3 OI conds met + degenerate price > 0 → triggered")
}

// TestOISurge_AnchoredToContractMonitorJS replicates a fixture matching the
// JS reference (references/user-snippets/contract-monitor.js L194-237) with
// config.minSurgePercentage=0.05, recentGrowth>=0.03, growingPeriods>=3.
// Validates 1:1 algorithm port for conditions 1-3 (cond 4 is SPEC-added).
func TestOISurge_AnchoredToContractMonitorJS(t *testing.T) {
	// Dip-then-rise series. JS hand-calc:
	//   minValue=90 (idx 2), currentValue=130, growthFromMin = (130-90)/90 ≈ 0.4444 ≥ 0.05 ✓
	//   recentStart = oi[10-6]=oi[4]=100, recentGrowth = (130-100)/100 = 0.30 ≥ 0.03 ✓
	//   growingPeriods (i 5..9): 105>100 ✓, 110>105 ✓, 115>110 ✓, 120>115 ✓, 130>120 ✓ = 5 ≥ 3 ✓
	//   isAlert = true
	oi := dseries(100, 95, 90, 95, 100, 105, 110, 115, 120, 130)
	r, err := OISurge(oi, d(51000), d(50000), OISurgeConfig{})
	require.NoError(t, err)
	assert.True(t, r.Triggered, "JS-anchored fixture must trigger")
	assert.InDelta(t, 0.4444, r.GrowthFromMin.InexactFloat64(), 0.001, "growthFromMin matches JS calc")
	assert.InDelta(t, 0.30, r.RecentGrowth.InexactFloat64(), 0.001, "recentGrowth matches JS calc")
	assert.Equal(t, 5, r.GrowingPeriods, "5 adjacent ↑ in last 6")
	assert.Equal(t, 5, r.RecentPeriodsCount, "6-1 pairs counted")
}
