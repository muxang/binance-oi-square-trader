package indicator

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// EMA computes Exponential Moving Average over `period` from oldest-first
// values, returning the latest EMA. Uses SMA seed + recursive smoothing:
//
//	EMA_(period-1) = SMA(values_0 .. values_(period-1))
//	multiplier     = 2 / (period + 1)
//	EMA_i          = (values_i - EMA_(i-1)) * multiplier + EMA_(i-1)   for i >= period
//
// ref: https://en.wikipedia.org/wiki/Moving_average#Exponential_moving_average
func EMA(values []decimal.Decimal, period int) (decimal.Decimal, error) {
	if period <= 0 {
		return decimal.Zero, fmt.Errorf("EMA: period must be > 0, got %d", period)
	}
	if len(values) < period {
		return decimal.Zero, fmt.Errorf("EMA: need >= %d values for period=%d, got %d", period, period, len(values))
	}
	periodDec := decimal.NewFromInt(int64(period))
	multiplier := decimal.NewFromInt(2).DivRound(periodDec.Add(decimal.NewFromInt(1)), decimalPrecision)

	sum := decimal.Zero
	for i := 0; i < period; i++ {
		sum = sum.Add(values[i])
	}
	ema := sum.DivRound(periodDec, decimalPrecision)
	for i := period; i < len(values); i++ {
		// Round at every step. Without this, Mul preserves all factor digits
		// and recursion grows precision by ~decimalPrecision per iteration —
		// 30 bars × period=20 ⇒ ~200 fractional digits in the result.
		// (ATR doesn't have this issue: its recursion ends in DivRound.)
		ema = values[i].Sub(ema).Mul(multiplier).Add(ema).Round(decimalPrecision)
	}
	return ema, nil
}
