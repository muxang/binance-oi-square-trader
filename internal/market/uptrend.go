package market

import "errors"

// UptrendItem is the 6-rule trend-discovery snapshot for one symbol.
// JSON tags wire directly to /api/admin/market/uptrend so callers don't need
// a parallel struct.
type UptrendItem struct {
	Symbol      string  `json:"symbol"`
	Close       float64 `json:"close"`
	EMA20       float64 `json:"ema20"`
	EMA50       float64 `json:"ema50"`
	Highest20   float64 `json:"highest20"` // max(high[1..20]), excluding current bar
	Volume      float64 `json:"volume"`
	VolumeMA20  float64 `json:"volume_ma20"`
	VolRatio    float64 `json:"vol_ratio"` // volume / volume_ma20
	RSI14       float64 `json:"rsi14"`
	ADX14       float64 `json:"adx14"`
	Pct4h       float64 `json:"pct_4h"` // (close / close[4 bars ago]) - 1
	BTCPct4h    float64 `json:"btc_pct_4h"`
	RelStrength float64 `json:"rel_strength"` // Pct4h - BTCPct4h

	CondEMAStack    bool `json:"cond_ema_stack"`
	CondBreakout    bool `json:"cond_breakout"`
	CondVolumeSurge bool `json:"cond_vol_surge"`
	CondRSI         bool `json:"cond_rsi"`
	CondADX         bool `json:"cond_adx"`
	CondRelStrength bool `json:"cond_rel_strength"`
	Pass            bool `json:"pass"`
}

// CheckUptrend evaluates the 6-rule filter on 1h klines (oldest→newest, ≥50 bars).
// Decisions per user-approved R.23 spec:
//
//	Q1 Donchian breakout EXCLUDES current bar  → highest(high[1..20])
//	Q2 volume_ma20 SMA INCLUDES current bar    → matches PineScript ta.sma
//	Q3 RSI/ADX use Wilder smoothing            → indicators.go RMA
//	Q4 4h_change is sliding 4-bar window       → PctChange(closes, 4)
func CheckUptrend(symbol string, closes, highs, lows, volumes []float64, btcPct4h float64) (UptrendItem, error) {
	n := len(closes)
	if n < 50 || len(highs) != n || len(lows) != n || len(volumes) != n {
		return UptrendItem{}, errors.New("uptrend: need ≥50 aligned bars")
	}
	item := UptrendItem{
		Symbol:   symbol,
		Close:    closes[n-1],
		Volume:   volumes[n-1],
		BTCPct4h: btcPct4h,
	}

	var err error
	if item.EMA20, err = EMA(closes, 20); err != nil {
		return item, err
	}
	if item.EMA50, err = EMA(closes, 50); err != nil {
		return item, err
	}
	item.CondEMAStack = item.Close > item.EMA20 && item.EMA20 > item.EMA50

	if item.Highest20, err = Highest(highs[:n-1], 20); err != nil {
		return item, err
	}
	item.CondBreakout = item.Close > item.Highest20

	if item.VolumeMA20, err = SMA(volumes, 20); err != nil {
		return item, err
	}
	if item.VolumeMA20 > 0 {
		item.VolRatio = item.Volume / item.VolumeMA20
	}
	item.CondVolumeSurge = item.VolRatio > 1.5

	if item.RSI14, err = RSI(closes, 14); err != nil {
		return item, err
	}
	item.CondRSI = item.RSI14 > 55

	if item.ADX14, err = ADX(highs, lows, closes, 14); err != nil {
		return item, err
	}
	item.CondADX = item.ADX14 > 20

	if item.Pct4h, err = PctChange(closes, 4); err != nil {
		return item, err
	}
	item.RelStrength = item.Pct4h - btcPct4h
	item.CondRelStrength = item.Pct4h > btcPct4h

	item.Pass = item.CondEMAStack && item.CondBreakout && item.CondVolumeSurge &&
		item.CondRSI && item.CondADX && item.CondRelStrength
	return item, nil
}
