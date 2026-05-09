// Package indicator provides technical indicator calculations on price slices.
//
// All functions accept plain decimal slices, decoupled from any data source
// (e.g. binance.KlineBar) so signal/decision/exit modules can reuse them
// without pulling in collector or binance packages.
package indicator

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// decimalPrecision is the digits-after-point used for divisions inside the
// indicator math. 18 matches the NUMERIC(36,18) money columns in postgres,
// so values round-tripped through DB don't lose precision.
const decimalPrecision = 18

// ATR computes Wilder's Average True Range over `period` from oldest-first
// OHLC slices, returning the latest ATR value.
//
// Algorithm (Wilder, 1978):
//
//	TR_i           = max(H_i - L_i, |H_i - C_(i-1)|, |L_i - C_(i-1)|)   for i >= 1
//	ATR_period     = SMA(TR_1 .. TR_period)
//	ATR_i          = (ATR_(i-1) * (period-1) + TR_i) / period           for i > period
//
// ref: https://en.wikipedia.org/wiki/Average_true_range
func ATR(highs, lows, closes []decimal.Decimal, period int) (decimal.Decimal, error) {
	if period <= 0 {
		return decimal.Zero, fmt.Errorf("ATR: period must be > 0, got %d", period)
	}
	n := len(highs)
	if len(lows) != n || len(closes) != n {
		return decimal.Zero, fmt.Errorf("ATR: length mismatch highs=%d lows=%d closes=%d", n, len(lows), len(closes))
	}
	if n < period+1 {
		return decimal.Zero, fmt.Errorf("ATR: need >= %d bars for period=%d, got %d", period+1, period, n)
	}
	periodDec := decimal.NewFromInt(int64(period))
	periodMinus1 := decimal.NewFromInt(int64(period - 1))

	// TR_i for i = 1..n-1 (TR_0 undefined: it would need a prev close).
	trs := make([]decimal.Decimal, n-1)
	for i := 1; i < n; i++ {
		hl := highs[i].Sub(lows[i])
		hc := highs[i].Sub(closes[i-1]).Abs()
		lc := lows[i].Sub(closes[i-1]).Abs()
		tr := hl
		if hc.GreaterThan(tr) {
			tr = hc
		}
		if lc.GreaterThan(tr) {
			tr = lc
		}
		trs[i-1] = tr
	}

	sum := decimal.Zero
	for i := 0; i < period; i++ {
		sum = sum.Add(trs[i])
	}
	atr := sum.DivRound(periodDec, decimalPrecision)
	for i := period; i < len(trs); i++ {
		atr = atr.Mul(periodMinus1).Add(trs[i]).DivRound(periodDec, decimalPrecision)
	}
	return atr, nil
}
