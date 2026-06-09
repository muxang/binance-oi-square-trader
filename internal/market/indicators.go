// Package market provides technical indicators matching TradingView / Binance
// K-line defaults: Wilder smoothing for RSI/ADX, SMA for moving averages.
//
// All functions take float64 slices ordered oldest→newest. Insufficient data
// returns ErrInsufficientData.
//
// ref: J.W. Wilder, "New Concepts in Technical Trading Systems" (1978).
package market

import (
	"errors"
	"math"
)

var (
	ErrInsufficientData = errors.New("indicators: insufficient data points")
	ErrInvalidPeriod    = errors.New("indicators: period must be > 0")
)

func SMA(values []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(values) < period {
		return 0, ErrInsufficientData
	}
	sum := 0.0
	for _, v := range values[len(values)-period:] {
		sum += v
	}
	return sum / float64(period), nil
}

func EMA(values []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(values) < period {
		return 0, ErrInsufficientData
	}
	alpha := 2.0 / float64(period+1)
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += values[i]
	}
	ema := seed / float64(period)
	for i := period; i < len(values); i++ {
		ema = values[i]*alpha + ema*(1-alpha)
	}
	return ema, nil
}

// RMA is Wilder's smoothing (alpha = 1/N), the building block for RSI and ADX.
func RMA(values []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(values) < period {
		return 0, ErrInsufficientData
	}
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += values[i]
	}
	rma := seed / float64(period)
	alpha := 1.0 / float64(period)
	for i := period; i < len(values); i++ {
		rma = values[i]*alpha + rma*(1-alpha)
	}
	return rma, nil
}

func RSI(closes []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(closes) < period+1 {
		return 0, ErrInsufficientData
	}
	gains := make([]float64, len(closes)-1)
	losses := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		ch := closes[i] - closes[i-1]
		if ch > 0 {
			gains[i-1] = ch
		} else {
			losses[i-1] = -ch
		}
	}
	avgGain, err := RMA(gains, period)
	if err != nil {
		return 0, err
	}
	avgLoss, err := RMA(losses, period)
	if err != nil {
		return 0, err
	}
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50, nil
		}
		return 100, nil
	}
	return 100 - 100/(1+avgGain/avgLoss), nil
}

// ADX (Wilder). ATR / +DM / -DM smoothed with RMA;
// DX = 100·|+DI − −DI| / (+DI + −DI); ADX = RMA(DX). Requires ≥ 2·period+1 bars.
func ADX(highs, lows, closes []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	n := len(highs)
	if n != len(lows) || n != len(closes) {
		return 0, errors.New("indicators: high/low/close length mismatch")
	}
	if n < 2*period+1 {
		return 0, ErrInsufficientData
	}
	tr := make([]float64, n-1)
	plusDM := make([]float64, n-1)
	minusDM := make([]float64, n-1)
	for i := 1; i < n; i++ {
		tr[i-1] = math.Max(highs[i]-lows[i], math.Max(
			math.Abs(highs[i]-closes[i-1]), math.Abs(lows[i]-closes[i-1])))
		up := highs[i] - highs[i-1]
		dn := lows[i-1] - lows[i]
		if up > dn && up > 0 {
			plusDM[i-1] = up
		}
		if dn > up && dn > 0 {
			minusDM[i-1] = dn
		}
	}
	atr, plusS, minusS := 0.0, 0.0, 0.0
	for i := 0; i < period; i++ {
		atr += tr[i]
		plusS += plusDM[i]
		minusS += minusDM[i]
	}
	atr /= float64(period)
	plusS /= float64(period)
	minusS /= float64(period)
	alpha := 1.0 / float64(period)
	dxSeries := make([]float64, 0, len(tr)-period+1)
	appendDX := func() {
		if atr == 0 {
			dxSeries = append(dxSeries, 0)
			return
		}
		plusDI := 100 * plusS / atr
		minusDI := 100 * minusS / atr
		sum := plusDI + minusDI
		if sum == 0 {
			dxSeries = append(dxSeries, 0)
			return
		}
		dxSeries = append(dxSeries, 100*math.Abs(plusDI-minusDI)/sum)
	}
	appendDX()
	for i := period; i < len(tr); i++ {
		atr = tr[i]*alpha + atr*(1-alpha)
		plusS = plusDM[i]*alpha + plusS*(1-alpha)
		minusS = minusDM[i]*alpha + minusS*(1-alpha)
		appendDX()
	}
	if len(dxSeries) < period {
		return 0, ErrInsufficientData
	}
	return RMA(dxSeries, period)
}

func Highest(values []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(values) < period {
		return 0, ErrInsufficientData
	}
	tail := values[len(values)-period:]
	maxv := tail[0]
	for _, v := range tail[1:] {
		if v > maxv {
			maxv = v
		}
	}
	return maxv, nil
}

// PctChange returns values[last] / values[last-lookback] - 1.
func PctChange(values []float64, lookback int) (float64, error) {
	if lookback <= 0 {
		return 0, ErrInvalidPeriod
	}
	if len(values) < lookback+1 {
		return 0, ErrInsufficientData
	}
	ref := values[len(values)-1-lookback]
	if ref == 0 {
		return 0, errors.New("indicators: reference value is zero")
	}
	return values[len(values)-1]/ref - 1, nil
}
