package signal

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// OISurgeConfig holds tunable thresholds for OI surge detection. v0.1
// defaults match SPEC §主信号 — conditions 1-3 anchored to references/
// user-snippets/contract-monitor.js L194-237; condition 4 is SPEC-added
// (see SPEC.md §主信号 L57 "60min 不接顶保护").
type OISurgeConfig struct {
	LookbackPeriods   int             // 默认 10, condition 1 minimum-point lookup
	RecentPeriods     int             // 默认 6,  conditions 2 & 3 window
	GrowthFromMinMin  decimal.Decimal // 默认 0.05 (5%), condition 1 threshold
	RecentGrowthMin   decimal.Decimal // 默认 0.03 (3%), condition 2 threshold
	MinGrowingPeriods int             // 默认 RecentPeriods/2 = 3, condition 3 minimum
}

// OISurgeResult is marshalled into signals.oi_data (JSONB). ARCH §7 declares
// oi_data as JSONB but doesn't specify internal structure — this struct is
// the Phase 2 v0.1 oi_data schema (snake_case JSON tags per SQL convention).
type OISurgeResult struct {
	Triggered          bool            `json:"triggered"`
	GrowthFromMin      decimal.Decimal `json:"growth_from_min"`
	RecentGrowth       decimal.Decimal `json:"recent_growth"`
	GrowingPeriods     int             `json:"growing_periods"`
	RecentPeriodsCount int             `json:"recent_periods_count"`
	PriceMovedUp       bool            `json:"price_moved_up"`
	FailedReason       string          `json:"failed_reason,omitempty"`
}

func oiSurgeDefaults(cfg OISurgeConfig) OISurgeConfig {
	if cfg.LookbackPeriods == 0 {
		cfg.LookbackPeriods = 10
	}
	if cfg.RecentPeriods == 0 {
		cfg.RecentPeriods = 6
	}
	if cfg.GrowthFromMinMin.IsZero() {
		cfg.GrowthFromMinMin = decimal.NewFromFloat(0.05)
	}
	if cfg.RecentGrowthMin.IsZero() {
		cfg.RecentGrowthMin = decimal.NewFromFloat(0.03)
	}
	if cfg.MinGrowingPeriods == 0 {
		cfg.MinGrowingPeriods = cfg.RecentPeriods / 2
	}
	return cfg
}

// OISurge computes the 4-condition OI surge verdict. Pure function — caller
// (signal_engine) fetches oi_history + klines, this just computes.
//
// Conditions (SPEC §主信号 L51-58):
//
//  1. growth_from_min   >= cfg.GrowthFromMinMin    (5% from last LookbackPeriods min)
//  2. recent_growth     >= cfg.RecentGrowthMin     (3% over last RecentPeriods)
//  3. growing_periods   >= cfg.MinGrowingPeriods   (>=3 of last 6 adjacent ↑)
//  4. close_now > close_minus_one_hour             (SPEC 追加, JS 无)
//
// oiSeries: latest at index len-1. closeNow / closeMinusOneHour: 5min closes.
// Returns (result, nil) on normal evaluation (Triggered or FailedReason set).
// Returns (zero, err) only on cfg invalidity.
func OISurge(
	oiSeries []decimal.Decimal,
	closeNow, closeMinusOneHour decimal.Decimal,
	cfg OISurgeConfig,
) (OISurgeResult, error) {
	cfg = oiSurgeDefaults(cfg)
	if cfg.LookbackPeriods <= 0 || cfg.RecentPeriods <= 0 {
		return OISurgeResult{}, errors.New("invalid cfg: LookbackPeriods/RecentPeriods must be > 0")
	}
	n := len(oiSeries)
	if n < cfg.RecentPeriods {
		return OISurgeResult{
			FailedReason: fmt.Sprintf("insufficient_oi_history: have %d need %d", n, cfg.RecentPeriods),
		}, nil
	}
	current := oiSeries[n-1]

	// Condition 1: growth from last LookbackPeriods min
	lookback := cfg.LookbackPeriods
	if lookback > n {
		lookback = n
	}
	minVal := oiSeries[n-lookback]
	for i := n - lookback + 1; i < n; i++ {
		if oiSeries[i].LessThan(minVal) {
			minVal = oiSeries[i]
		}
	}
	if minVal.IsZero() {
		return OISurgeResult{FailedReason: "zero_min_oi"}, nil
	}
	growthFromMin := current.Sub(minVal).Div(minVal)

	// Condition 2: recent overall growth
	recent := cfg.RecentPeriods
	if recent > n {
		recent = n
	}
	recentStart := oiSeries[n-recent]
	if recentStart.IsZero() {
		return OISurgeResult{FailedReason: "zero_recent_start_oi"}, nil
	}
	recentGrowth := current.Sub(recentStart).Div(recentStart)

	// Condition 3: growing periods (adjacent ↑ count in last `recent` window)
	growingPeriods := 0
	for i := n - recent + 1; i < n; i++ {
		if oiSeries[i].GreaterThan(oiSeries[i-1]) {
			growingPeriods++
		}
	}

	// Condition 4: SPEC-added price-moved-up guard
	priceMovedUp := closeNow.GreaterThan(closeMinusOneHour)

	result := OISurgeResult{
		GrowthFromMin:      growthFromMin,
		RecentGrowth:       recentGrowth,
		GrowingPeriods:     growingPeriods,
		RecentPeriodsCount: recent - 1,
		PriceMovedUp:       priceMovedUp,
	}
	switch {
	case growthFromMin.LessThan(cfg.GrowthFromMinMin):
		result.FailedReason = "low_growth_from_min"
	case recentGrowth.LessThan(cfg.RecentGrowthMin):
		result.FailedReason = "recent_flat"
	case growingPeriods < cfg.MinGrowingPeriods:
		result.FailedReason = "no_uptrend"
	case !priceMovedUp:
		result.FailedReason = "price_not_moved_up"
	default:
		result.Triggered = true
	}
	return result, nil
}
