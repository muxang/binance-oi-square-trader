package decision

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
)

// d builds decimal from float for fixtures (NOT for prod money math).
func dec(v float64) decimal.Decimal { return decimal.NewFromFloat(v) }

// btcFilters / pepeFilters — realistic binance USDⓈ-M perp filter values.
var (
	btcFilters = binance.TradingFilters{
		StepSize:    dec(0.001),
		MinQty:      dec(0.001),
		MinNotional: decimal.NewFromInt(5),
		TickSize:    dec(0.10),
	}
	// PEPEUSDT-style: low price, large step (qty in millions for $500 notional)
	pepeFilters = binance.TradingFilters{
		StepSize:    decimal.NewFromInt(10000),
		MinQty:      decimal.NewFromInt(10000),
		MinNotional: decimal.NewFromInt(5),
		TickSize:    dec(0.0000001),
	}
)

// --- 4 main paths ---

func TestSize_FullBTC(t *testing.T) {
	r, err := SizeTrade("entered_full", decimal.NewFromInt(80000), btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.True(t, r.OK)
	// 50 × 10 / 80000 = 0.00625 → floor to 0.001 step → 0.006
	assert.True(t, r.Quantity.Equal(dec(0.006)), "BTC full qty=0.006, got %s", r.Quantity)
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(480)), "actualNotional = 0.006 × 80000 = 480, got %s", r.Notional)
	assert.True(t, r.TargetNotional.Equal(decimal.NewFromInt(500)), "target = 50 × 10")
	assert.True(t, r.Margin.Equal(decimal.NewFromInt(50)))
	assert.True(t, r.EntryPrice.Equal(decimal.NewFromInt(80000)))
	assert.EqualValues(t, 10, r.Leverage)
}

func TestSize_HalfBTC(t *testing.T) {
	r, err := SizeTrade("entered_half", decimal.NewFromInt(80000), btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.True(t, r.OK)
	// 25 × 10 / 80000 = 0.003125 → floor 0.001 → 0.003
	assert.True(t, r.Quantity.Equal(dec(0.003)))
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(240))) // 0.003 × 80000
	assert.True(t, r.Margin.Equal(decimal.NewFromInt(25)))
}

func TestSize_FullPEPE(t *testing.T) {
	// Low-price symbol, qty 50M scale, no step偏差
	r, err := SizeTrade("entered_full", dec(0.00001), pepeFilters, SizingConfig{})
	require.NoError(t, err)
	assert.True(t, r.OK)
	assert.True(t, r.Quantity.Equal(decimal.NewFromInt(50_000_000)))
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(500)), "PEPE step round 0 偏差")
}

func TestSize_HalfPEPE(t *testing.T) {
	r, err := SizeTrade("entered_half", dec(0.00001), pepeFilters, SizingConfig{})
	require.NoError(t, err)
	assert.True(t, r.OK)
	assert.True(t, r.Quantity.Equal(decimal.NewFromInt(25_000_000)))
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(250)))
}

// --- 3 boundary ---

func TestSize_QtyBelowMinQty_Reject(t *testing.T) {
	// price 1,000,000 → qty raw = 500/1e6 = 0.0005 < MinQty 0.001 → rejected
	r, err := SizeTrade("entered_full", decimal.NewFromInt(1_000_000), btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Equal(t, SizingReasonBelowMinQty, r.Reason)
	assert.True(t, r.Quantity.LessThan(btcFilters.MinQty), "diag fields populated even on reject")
}

func TestSize_NotionalBelowMin_Reject(t *testing.T) {
	// stepSize=1, price=499 → qty=floor(500/499)=1 → notional=499 < MinNotional 1000
	filters := binance.TradingFilters{
		StepSize: decimal.NewFromInt(1), MinQty: decimal.NewFromInt(1),
		MinNotional: decimal.NewFromInt(1000),
	}
	r, err := SizeTrade("entered_full", decimal.NewFromInt(499), filters, SizingConfig{})
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Equal(t, SizingReasonBelowMinNotional, r.Reason)
	assert.True(t, r.Quantity.Equal(decimal.NewFromInt(1)))
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(499)))
}

func TestSize_StepRoundExactBoundary(t *testing.T) {
	// price=500000 → qty raw=500/500000=0.001 → floor 0.001 step → 0.001 (== MinQty, just passes)
	r, err := SizeTrade("entered_full", decimal.NewFromInt(500_000), btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.True(t, r.OK, "qty == MinQty should pass (LessThan strict)")
	assert.True(t, r.Quantity.Equal(dec(0.001)))
	assert.True(t, r.Notional.Equal(decimal.NewFromInt(500)))
}

// --- 2 business reject ---

func TestSize_InvalidDecision_Reject(t *testing.T) {
	r, err := SizeTrade("rejected", decimal.NewFromInt(80000), btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Equal(t, SizingReasonInvalidDecision, r.Reason)
}

func TestSize_ZeroPrice_Reject(t *testing.T) {
	r, err := SizeTrade("entered_full", decimal.Zero, btcFilters, SizingConfig{})
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Equal(t, SizingReasonZeroPrice, r.Reason)
}

// --- 2 invariant err (programmer error) ---

func TestSize_NegativeLeverage_ReturnsErr(t *testing.T) {
	cfg := SizingConfig{Leverage: -5, FullMarginUSDT: decimal.NewFromInt(50), HalfMarginUSDT: decimal.NewFromInt(25)}
	_, err := SizeTrade("entered_full", decimal.NewFromInt(80000), btcFilters, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Leverage")
}

func TestSize_NegativeMargin_ReturnsErr(t *testing.T) {
	cfg := SizingConfig{FullMarginUSDT: decimal.NewFromInt(-1), HalfMarginUSDT: decimal.NewFromInt(25), Leverage: 10}
	_, err := SizeTrade("entered_full", decimal.NewFromInt(80000), btcFilters, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "margins")
}

// Round 2.z+ bugfix verify: SizingResult exposes StepSize so executor.placeOneTP
// can round partial TP qty to symbol's LOT_SIZE. Pre-fix the field didn't exist
// and TP qty stayed fractional → Binance -1111 for ARPA-class symbols (stepSize=1).
func TestSize_ResultExposesStepSize(t *testing.T) {
	// btcFilters has StepSize 0.001
	r, err := SizeTrade("entered_full", decimal.NewFromInt(80000), btcFilters, SizingConfig{
		FullMarginUSDT: decimal.NewFromInt(50), HalfMarginUSDT: decimal.NewFromInt(25), Leverage: 10,
	})
	require.NoError(t, err)
	assert.True(t, r.StepSize.Equal(btcFilters.StepSize), "StepSize plumbed through to SizingResult")

	// ARPA-class: stepSize=1
	arpaFilters := binance.TradingFilters{
		StepSize: decimal.NewFromInt(1), MinQty: decimal.NewFromInt(1),
		MinNotional: decimal.NewFromInt(5), TickSize: decimal.NewFromFloat(0.00001),
	}
	r2, err := SizeTrade("entered_full", decimal.NewFromFloat(0.011640), arpaFilters, SizingConfig{
		FullMarginUSDT: decimal.NewFromInt(25), HalfMarginUSDT: decimal.NewFromInt(12), Leverage: 5,
	})
	require.NoError(t, err)
	assert.True(t, r2.StepSize.Equal(decimal.NewFromInt(1)), "ARPA stepSize=1 exposed for TP qty rounding")
}
