package market

import (
	"errors"
	"math"
	"time"
)

// UptrendItem captures the R.24 composite signal output per user spec:
//
//	pass = baseTrendOk  AND  relativeStrengthOk  AND  ( breakoutSignal  OR  pullbackSignal )
//
// JSON tags wire directly to /api/admin/market/uptrend.
type UptrendItem struct {
	Symbol string `json:"symbol"`

	// 1h snapshot — values from the latest CLOSED 1h bar (collector strips incomplete)
	Close      float64 `json:"close"`       // latest1hClose
	Low        float64 `json:"low"`         // latest1hLow
	Volume     float64 `json:"volume"`      // latest1hVolume — quote preferred, base fallback
	VolumeMA20 float64 `json:"volume_ma20"` // average of PREVIOUS 20 closed 1h bars (excludes current)
	Highest20  float64 `json:"highest20"`   // max(high) over PREVIOUS 20 closed 1h bars

	// 1h indicators (use ALL closed bars including current)
	EMA20      float64 `json:"ema20"`
	EMA20Prev3 float64 `json:"ema20_3bars_ago"` // EMA20 evaluated 3 bars ago — slope check
	EMA50      float64 `json:"ema50"`
	RSI14      float64 `json:"rsi14"`
	ADX14      float64 `json:"adx14"`
	PlusDI14   float64 `json:"plus_di14"`
	MinusDI14  float64 `json:"minus_di14"`

	// 4h snapshot + indicator (multi-timeframe confirmation)
	Close4h  float64 `json:"close_4h"`
	EMA20_4h float64 `json:"ema20_4h"`

	// 4h relative strength (single-bar pct change of the latest CLOSED 4h candle)
	Pct4h       float64 `json:"pct_4h"`       // (latest4h.close − latest4h.open) / latest4h.open  (decimal)
	BTCPct4h    float64 `json:"btc_pct_4h"`   // same formula on BTCUSDT
	RelStrength float64 `json:"rel_strength"` // pct_4h − btc_pct_4h  (decimal)

	// Derived display ratios
	BreakoutRatio float64 `json:"breakout_ratio"` // close / highest20  (>1.002 ⇒ break)
	VolRatio      float64 `json:"vol_ratio"`     // volume / volume_ma20
	CloseToEMA20  float64 `json:"close_to_ema20"` // close / ema20
	CloseToEMA50  float64 `json:"close_to_ema50"` // close / ema50

	// baseTrend (must pass) — 4 sub-conditions
	CondCloseAboveEMA50      bool `json:"cond_close_above_ema50"`
	CondEMA20AboveEMA50      bool `json:"cond_ema20_above_ema50"`
	CondEMA20Rising          bool `json:"cond_ema20_rising"`
	CondMTFClose4hAboveEMA20 bool `json:"cond_mtf_close4h_above_ema20"`
	CondBaseTrend            bool `json:"cond_base_trend"`

	// relStrength (must pass)
	CondRelStrength bool `json:"cond_rel_strength"` // pct_4h >= btc_pct_4h − 0.005

	// breakoutSignal (alternative 1) — 5 sub-conditions
	CondBreakoutHigh   bool `json:"cond_breakout_high"`     // close > Highest20 · 1.002
	CondBreakoutVol    bool `json:"cond_breakout_vol"`      // vol > VolMA20 · 1.5
	CondBreakoutRSI    bool `json:"cond_breakout_rsi"`      // RSI > 55
	CondBreakoutADX    bool `json:"cond_breakout_adx"`      // ADX > 20
	CondBreakoutDIPlus bool `json:"cond_breakout_di_plus"`  // +DI > −DI (direction up)
	CondBreakout       bool `json:"cond_breakout"`

	// pullbackSignal (alternative 2) — 6 sub-conditions (RSI band split)
	CondPullbackClose      bool `json:"cond_pullback_close"`       // close >= EMA20 · 0.98
	CondPullbackLow        bool `json:"cond_pullback_low"`         // low <= EMA20 · 1.01
	CondPullbackAboveEMA50 bool `json:"cond_pullback_above_ema50"` // close > EMA50
	CondPullbackRSIMin     bool `json:"cond_pullback_rsi_min"`     // RSI > 45 (not weak)
	CondPullbackRSIMax     bool `json:"cond_pullback_rsi_max"`     // RSI < 70 (not overheated)
	CondPullbackVol        bool `json:"cond_pullback_vol"`         // vol <= VolMA20 · 1.3
	CondPullback           bool `json:"cond_pullback"`

	Pass        bool      `json:"pass"`        // finalSignal
	SignalType  string    `json:"signal_type"` // BREAKOUT | PULLBACK | BREAKOUT_AND_PULLBACK | NONE
	TriggerTime time.Time `json:"trigger_time"`
}

// Thresholds — hardcoded per R.24 user spec.
const (
	relStrengthTolerance = -0.005 // token_pct ≥ btc_pct − 0.5pp (stored as decimal)

	breakoutHighBuffer = 1.002
	breakoutVolMulti   = 1.5
	breakoutRSIMin     = 55.0
	breakoutADXMin     = 20.0

	pullbackEMA20Floor = 0.98
	pullbackLowTouch   = 1.01
	pullbackRSIMin     = 45.0
	pullbackRSIMax     = 70.0
	pullbackVolMax     = 1.3
)

func anyNaN(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return true
		}
	}
	return false
}

// CheckUptrend evaluates the R.24 composite filter on closed-bar data.
//
//	closes1h, highs, lows, volumes:  ≥50 aligned closed 1h bars (oldest→newest)
//	closes4h:                        ≥21 closed 4h bars (oldest→newest)
//	tokenPct4h:                      (latest_4h.close − latest_4h.open) / latest_4h.open
//	btcPct4h:                        same formula on BTCUSDT — precomputed by collector
//
// Per-symbol skip (returns error): insufficient bars / indicator NaN / VolumeMA20 ≤ 0.
func CheckUptrend(
	symbol string,
	closes1h, highs, lows, volumes []float64,
	closes4h []float64,
	tokenPct4h, btcPct4h float64,
) (UptrendItem, error) {
	n := len(closes1h)
	if n < 50 || len(highs) != n || len(lows) != n || len(volumes) != n {
		return UptrendItem{}, errors.New("uptrend: need ≥50 aligned 1h bars")
	}
	if len(closes4h) < 21 {
		return UptrendItem{}, errors.New("uptrend: need ≥21 4h bars")
	}

	item := UptrendItem{
		Symbol:   symbol,
		Close:    closes1h[n-1],
		Low:      lows[n-1],
		Close4h:  closes4h[len(closes4h)-1],
		Volume:   volumes[n-1],
		Pct4h:    tokenPct4h,
		BTCPct4h: btcPct4h,
	}

	var err error
	if item.EMA20, err = EMA(closes1h, 20); err != nil {
		return item, err
	}
	// EMA20[3] — Pine notation for EMA20 evaluated 3 bars ago. Pass closes
	// excluding the last 3; the function's final value is EMA20 at index n-4.
	if item.EMA20Prev3, err = EMA(closes1h[:n-3], 20); err != nil {
		return item, err
	}
	if item.EMA50, err = EMA(closes1h, 50); err != nil {
		return item, err
	}
	if item.EMA20_4h, err = EMA(closes4h, 20); err != nil {
		return item, err
	}
	// previousHighestHigh20 — exclude current bar (the latest closed).
	if item.Highest20, err = Highest(highs[:n-1], 20); err != nil {
		return item, err
	}
	// volumeMA20 — exclude current bar (per user spec §八.3).
	if item.VolumeMA20, err = SMA(volumes[:n-1], 20); err != nil {
		return item, err
	}
	if item.RSI14, err = RSI(closes1h, 14); err != nil {
		return item, err
	}
	if item.ADX14, item.PlusDI14, item.MinusDI14, err = DMI(highs, lows, closes1h, 14); err != nil {
		return item, err
	}

	// User spec §八.5: any NaN/Inf in indicators ⇒ skip the symbol.
	if anyNaN(item.EMA20, item.EMA20Prev3, item.EMA50, item.EMA20_4h,
		item.Highest20, item.VolumeMA20, item.RSI14,
		item.ADX14, item.PlusDI14, item.MinusDI14) {
		return item, errors.New("uptrend: indicator NaN/Inf, skip")
	}
	// User spec §八.6: VolumeMA20 ≤ 0 ⇒ skip.
	if item.VolumeMA20 <= 0 {
		return item, errors.New("uptrend: volume_ma20 ≤ 0, skip")
	}

	item.VolRatio = item.Volume / item.VolumeMA20
	if item.Highest20 > 0 {
		item.BreakoutRatio = item.Close / item.Highest20
	}
	if item.EMA20 > 0 {
		item.CloseToEMA20 = item.Close / item.EMA20
	}
	if item.EMA50 > 0 {
		item.CloseToEMA50 = item.Close / item.EMA50
	}
	item.RelStrength = item.Pct4h - btcPct4h

	// baseTrend
	item.CondCloseAboveEMA50      = item.Close > item.EMA50
	item.CondEMA20AboveEMA50      = item.EMA20 > item.EMA50
	item.CondEMA20Rising          = item.EMA20 > item.EMA20Prev3
	item.CondMTFClose4hAboveEMA20 = item.Close4h > item.EMA20_4h
	item.CondBaseTrend = item.CondCloseAboveEMA50 &&
		item.CondEMA20AboveEMA50 &&
		item.CondEMA20Rising &&
		item.CondMTFClose4hAboveEMA20

	// relStrength
	item.CondRelStrength = item.Pct4h >= btcPct4h+relStrengthTolerance

	// breakoutSignal
	item.CondBreakoutHigh   = item.Close > item.Highest20*breakoutHighBuffer
	item.CondBreakoutVol    = item.VolRatio > breakoutVolMulti
	item.CondBreakoutRSI    = item.RSI14 > breakoutRSIMin
	item.CondBreakoutADX    = item.ADX14 > breakoutADXMin
	item.CondBreakoutDIPlus = item.PlusDI14 > item.MinusDI14
	item.CondBreakout = item.CondBreakoutHigh && item.CondBreakoutVol &&
		item.CondBreakoutRSI && item.CondBreakoutADX && item.CondBreakoutDIPlus

	// pullbackSignal
	item.CondPullbackClose      = item.Close >= item.EMA20*pullbackEMA20Floor
	item.CondPullbackLow        = item.Low <= item.EMA20*pullbackLowTouch
	item.CondPullbackAboveEMA50 = item.Close > item.EMA50
	item.CondPullbackRSIMin     = item.RSI14 > pullbackRSIMin
	item.CondPullbackRSIMax     = item.RSI14 < pullbackRSIMax
	item.CondPullbackVol        = item.VolRatio <= pullbackVolMax
	item.CondPullback = item.CondPullbackClose && item.CondPullbackLow &&
		item.CondPullbackAboveEMA50 && item.CondPullbackRSIMin &&
		item.CondPullbackRSIMax && item.CondPullbackVol

	// signalType — independent of baseTrend/relStrength (sub-status for debugging)
	switch {
	case item.CondBreakout && item.CondPullback:
		item.SignalType = "BREAKOUT_AND_PULLBACK"
	case item.CondBreakout:
		item.SignalType = "BREAKOUT"
	case item.CondPullback:
		item.SignalType = "PULLBACK"
	default:
		item.SignalType = "NONE"
	}

	// finalSignal — pass = baseTrend AND relStrength AND (breakout OR pullback)
	item.Pass = item.CondBaseTrend && item.CondRelStrength &&
		(item.CondBreakout || item.CondPullback)

	return item, nil
}
