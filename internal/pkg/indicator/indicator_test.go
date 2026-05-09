package indicator

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func decSlice(strs ...string) []decimal.Decimal {
	out := make([]decimal.Decimal, len(strs))
	for i, s := range strs {
		out[i] = decimal.RequireFromString(s)
	}
	return out
}

// fractionalDigits returns the digits after '.' in s.String(), or "" if integer.
func fractionalDigits(s string) string {
	if idx := strings.IndexByte(s, '.'); idx >= 0 {
		return s[idx+1:]
	}
	return ""
}

// TestATR_KnownValues_HandComputed validates Wilder smoothing against
// hand-computed values that round to exact decimals (no precision-loss
// concerns hiding bugs). Period=2 is unrealistic but lets us pick numbers
// whose ATR is exact at every step.
//
// Bars 0..4: H=[10,12,14,16,20], L=[8,10,12,14,16], C=[9,11,13,15,18]
//
//	TR_1 = max(12-10, |12-9|,  |10-9|)  = max(2, 3, 1) = 3
//	TR_2 = max(14-12, |14-11|, |12-11|) = max(2, 3, 1) = 3
//	TR_3 = max(16-14, |16-13|, |14-13|) = max(2, 3, 1) = 3
//	TR_4 = max(20-16, |20-15|, |16-15|) = max(4, 5, 1) = 5
//	ATR_2(seed) = (3+3)/2     = 3
//	ATR_3       = (3*1 + 3)/2 = 3
//	ATR_4       = (3*1 + 5)/2 = 4
func TestATR_KnownValues_HandComputed(t *testing.T) {
	highs := decSlice("10", "12", "14", "16", "20")
	lows := decSlice("8", "10", "12", "14", "16")
	closes := decSlice("9", "11", "13", "15", "18")
	atr, err := ATR(highs, lows, closes, 2)
	require.NoError(t, err)
	assert.True(t, atr.Equal(dec("4")), "want 4, got %s", atr)
}

// TestATR_KnownValues_ConstantTR_Period14: 16 bars constructed so every TR=1.5
// (H_i=10+i, L_i=9+i, C_i=9.5+i). ATR_14 must equal 1.5 exactly because
// constant input is invariant under both SMA seed and Wilder smoothing.
func TestATR_KnownValues_ConstantTR_Period14(t *testing.T) {
	const n = 16
	highs := make([]decimal.Decimal, n)
	lows := make([]decimal.Decimal, n)
	closes := make([]decimal.Decimal, n)
	for i := 0; i < n; i++ {
		highs[i] = decimal.NewFromInt(int64(10 + i))
		lows[i] = decimal.NewFromInt(int64(9 + i))
		closes[i] = decimal.NewFromInt(int64(9 + i)).Add(dec("0.5"))
	}
	atr, err := ATR(highs, lows, closes, 14)
	require.NoError(t, err)
	assert.True(t, atr.Equal(dec("1.5")), "want 1.5, got %s", atr)
}

// TestATR_DecimalPrecision uses 18-digit OHLC values float64 (~15 sig digits)
// cannot represent exactly. If any float64 round-trip happens on the path,
// the result loses fractional digits.
func TestATR_DecimalPrecision(t *testing.T) {
	highs := decSlice("80123.456789012345", "80223.456789012345", "80323.456789012345")
	lows := decSlice("80000.123456789012", "80100.123456789012", "80200.123456789012")
	closes := decSlice("80050.987654321098", "80150.987654321098", "80250.987654321098")
	atr, err := ATR(highs, lows, closes, 2)
	require.NoError(t, err)
	frac := fractionalDigits(atr.String())
	assert.GreaterOrEqual(t, len(frac), 10, "lost precision, atr=%s", atr)
}

func TestATR_Period14_NeedsAtLeast15Bars(t *testing.T) {
	highs := make([]decimal.Decimal, 14)
	lows := make([]decimal.Decimal, 14)
	closes := make([]decimal.Decimal, 14)
	for i := range highs {
		highs[i] = decimal.NewFromInt(int64(10 + i))
		lows[i] = decimal.NewFromInt(int64(9 + i))
		closes[i] = decimal.NewFromInt(int64(9 + i))
	}
	_, err := ATR(highs, lows, closes, 14)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need >= 15")
}

func TestATR_LengthMismatch_Error(t *testing.T) {
	_, err := ATR(decSlice("1", "2", "3"), decSlice("1", "2"), decSlice("1", "2", "3"), 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "length mismatch")
}

func TestATR_PeriodTooLarge_Error(t *testing.T) {
	highs := decSlice("1", "2", "3")
	_, err := ATR(highs, highs, highs, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need >= 101")
}

func TestATR_PeriodInvalid_Error(t *testing.T) {
	highs := decSlice("1", "2")
	_, err := ATR(highs, highs, highs, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "period must be > 0")
}

// TestEMA_KnownValues_HandComputed:
// values = [1..7], period=4, multiplier = 2/5 = 0.4
//
//	SMA seed = (1+2+3+4)/4         = 2.5
//	EMA[3]   = 2.5
//	EMA[4]   = (5 - 2.5)*0.4 + 2.5 = 3.5
//	EMA[5]   = (6 - 3.5)*0.4 + 3.5 = 4.5
//	EMA[6]   = (7 - 4.5)*0.4 + 4.5 = 5.5
func TestEMA_KnownValues_HandComputed(t *testing.T) {
	values := decSlice("1", "2", "3", "4", "5", "6", "7")
	ema, err := EMA(values, 4)
	require.NoError(t, err)
	assert.True(t, ema.Equal(dec("5.5")), "want 5.5, got %s", ema)
}

func TestEMA_DecimalPrecision(t *testing.T) {
	values := decSlice("80123.456789012345", "80223.456789012345", "80323.456789012345", "80423.456789012345")
	ema, err := EMA(values, 2)
	require.NoError(t, err)
	frac := fractionalDigits(ema.String())
	assert.GreaterOrEqual(t, len(frac), 10, "lost precision, ema=%s", ema)
}

func TestEMA_Period20_NeedsAtLeast20Bars(t *testing.T) {
	values := make([]decimal.Decimal, 19)
	for i := range values {
		values[i] = decimal.NewFromInt(int64(i + 1))
	}
	_, err := EMA(values, 20)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need >= 20")
}

func TestEMA_PeriodTooLarge_Error(t *testing.T) {
	_, err := EMA(decSlice("1", "2", "3"), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need >= 100")
}

func TestEMA_PeriodInvalid_Error(t *testing.T) {
	_, err := EMA(decSlice("1", "2"), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "period must be > 0")
}

func TestEMA_AllZeros_ReturnsZero(t *testing.T) {
	ema, err := EMA(decSlice("0", "0", "0", "0", "0"), 3)
	require.NoError(t, err)
	assert.True(t, ema.IsZero(), "want 0, got %s", ema)
}

// TestEMA_VaryingInput_PrecisionBounded regression-tests a bug found in 1.3
// real-data verification: with varying input, EMA's recursion (Sub/Mul/Add)
// grows precision unboundedly unless we Round at each step. Real Binance
// 15m closes produced ~200 fractional digits before the fix.
func TestEMA_VaryingInput_PrecisionBounded(t *testing.T) {
	values := make([]decimal.Decimal, 30)
	for i := range values {
		values[i] = decimal.NewFromInt(int64(80000 + i*7)) // varying, non-trivial multipliers
	}
	ema, err := EMA(values, 20)
	require.NoError(t, err)
	frac := fractionalDigits(ema.String())
	assert.LessOrEqual(t, len(frac), decimalPrecision+2,
		"EMA precision must be bounded near decimalPrecision (%d), got %d digits: %s",
		decimalPrecision, len(frac), ema)
}
