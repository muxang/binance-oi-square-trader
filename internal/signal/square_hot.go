package signal

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// SquareHotMode classifies which adaptive algorithm variant was used.
type SquareHotMode string

const (
	ModeFallback SquareHotMode = "fallback" // < MinDataPoints (< 2h)
	ModeShort    SquareHotMode = "short"    // 2-6h
	ModeMedium   SquareHotMode = "medium"   // 6-24h
	ModeStandard SquareHotMode = "standard" // ≥ 24h
)

// SquareHotConfig — see SPEC §辅助信号. v0.1 defaults are empirical.
type SquareHotConfig struct {
	StandardRatioThreshold        decimal.Decimal // 默认 2.0
	StandardAccelerationThreshold decimal.Decimal // 默认 0.6
	MediumRatioThreshold          decimal.Decimal // 默认 2.0
	MediumAccelerationThreshold   decimal.Decimal // 默认 0.6
	ShortRatioThreshold           decimal.Decimal // 默认 2.5
	MinDataPoints                 int             // 默认 8 (= 2h × 4/h)
	SamplePeriod                  time.Duration   // 默认 15min (T3 cron)
}

// SquareHotResult marshals into signals.square_data (JSONB). v0.1 schema —
// ARCH §7 declares column type only; this struct defines internal structure.
type SquareHotResult struct {
	Hot           bool            `json:"hot"`
	Mode          SquareHotMode   `json:"mode"`
	Ratio         decimal.Decimal `json:"ratio"`
	Acceleration  decimal.Decimal `json:"acceleration"`
	SampleCount   int             `json:"sample_count"`
	DataSpanHours decimal.Decimal `json:"data_span_hours"`
	FailedReason  string          `json:"failed_reason,omitempty"`
}

func squareHotDefaults(cfg SquareHotConfig) SquareHotConfig {
	if cfg.StandardRatioThreshold.IsZero() {
		cfg.StandardRatioThreshold = decimal.NewFromFloat(2.0)
	}
	if cfg.StandardAccelerationThreshold.IsZero() {
		cfg.StandardAccelerationThreshold = decimal.NewFromFloat(0.6)
	}
	if cfg.MediumRatioThreshold.IsZero() {
		cfg.MediumRatioThreshold = decimal.NewFromFloat(2.0)
	}
	if cfg.MediumAccelerationThreshold.IsZero() {
		cfg.MediumAccelerationThreshold = decimal.NewFromFloat(0.6)
	}
	if cfg.ShortRatioThreshold.IsZero() {
		cfg.ShortRatioThreshold = decimal.NewFromFloat(2.5)
	}
	if cfg.MinDataPoints == 0 {
		cfg.MinDataPoints = 8
	}
	if cfg.SamplePeriod == 0 {
		cfg.SamplePeriod = 15 * time.Minute
	}
	return cfg
}

// SquareHot computes the adaptive curvature-based hot verdict. Pure function —
// caller (signal_engine) fetches contentCount from square_hashtag_history,
// equal-spaced samples (ORDER BY ts ASC, default 15min via cfg.SamplePeriod).
//
// Modes (SPEC §辅助信号):
//   - standard (≥24h): 60min Δ + ratio + accel(window=6 Δ → 4 Δ”)
//   - medium   (6-24h): 60min Δ + ratio + accel(window=4 Δ → 2 Δ”; pos_ratio∈{0,0.5,1})
//   - short    (2-6h):  15min δ + ratio only (阈值 2.5 更严)
//   - fallback (<2h):   hot=false
//
// Defensive mode downgrade: if accel data insufficient for the picked mode,
// drop down a level so SquareHotResult.Mode reflects the actually-used algorithm.
func SquareHot(contentCounts []decimal.Decimal, cfg SquareHotConfig) (SquareHotResult, error) {
	cfg = squareHotDefaults(cfg)
	for _, t := range []decimal.Decimal{
		cfg.StandardRatioThreshold, cfg.StandardAccelerationThreshold,
		cfg.MediumRatioThreshold, cfg.MediumAccelerationThreshold, cfg.ShortRatioThreshold,
	} {
		if t.IsNegative() {
			return SquareHotResult{}, errors.New("invalid cfg: thresholds must be >= 0")
		}
	}

	n := len(contentCounts)
	spanSamples := n - 1
	if spanSamples < 0 {
		spanSamples = 0
	}
	spanHours := decimal.NewFromInt(int64(spanSamples)).Mul(decimal.NewFromFloat(cfg.SamplePeriod.Hours()))
	if n < cfg.MinDataPoints {
		return SquareHotResult{
			Mode: ModeFallback, SampleCount: n, DataSpanHours: spanHours,
			FailedReason: fmt.Sprintf("insufficient_samples: have %d need %d", n, cfg.MinDataPoints),
		}, nil
	}

	periodsIn60Min := int(time.Hour / cfg.SamplePeriod)
	if periodsIn60Min < 1 {
		periodsIn60Min = 1
	}

	// 1. Pick mode by span
	var mode SquareHotMode
	switch {
	case spanHours.LessThan(decimal.NewFromInt(6)):
		mode = ModeShort
	case spanHours.LessThan(decimal.NewFromInt(24)):
		mode = ModeMedium
	default:
		mode = ModeStandard
	}

	// 2. Defensive downgrade if accel data insufficient (rare; defends against
	// trader-restart + sparse-history edge cases per CLAUDE.md §15)
	deltas60Count := n - periodsIn60Min
	if mode == ModeStandard && deltas60Count < 6 {
		mode = ModeMedium
	}
	if mode == ModeMedium && deltas60Count < 4 {
		mode = ModeShort
	}

	// 3. Mode-specific params
	var (
		deltas                         []decimal.Decimal
		recentK, accelWindow           int
		ratioThreshold, accelThreshold decimal.Decimal
		useLast24h                     bool
	)
	switch mode {
	case ModeStandard:
		deltas = computeNPeriodDeltas(contentCounts, periodsIn60Min)
		recentK, accelWindow = 3, 6
		ratioThreshold, accelThreshold = cfg.StandardRatioThreshold, cfg.StandardAccelerationThreshold
		useLast24h = true
	case ModeMedium:
		deltas = computeNPeriodDeltas(contentCounts, periodsIn60Min)
		recentK, accelWindow = 3, 4
		ratioThreshold, accelThreshold = cfg.MediumRatioThreshold, cfg.MediumAccelerationThreshold
	case ModeShort:
		deltas = computeNPeriodDeltas(contentCounts, 1)
		recentK = 6
		ratioThreshold = cfg.ShortRatioThreshold
	}

	if len(deltas) <= recentK {
		return SquareHotResult{
			Mode: mode, SampleCount: n, DataSpanHours: spanHours,
			FailedReason: fmt.Sprintf("insufficient_deltas: have %d need >%d", len(deltas), recentK),
		}, nil
	}

	// 4. Slice baseline (last 24h truncation for standard) + recent
	baselineRaw := deltas[:len(deltas)-recentK]
	if useLast24h {
		samplesIn24h := int((24 * time.Hour) / cfg.SamplePeriod)
		deltaLookback := samplesIn24h - periodsIn60Min
		if len(baselineRaw) > deltaLookback {
			baselineRaw = baselineRaw[len(baselineRaw)-deltaLookback:]
		}
	}
	recent := deltas[len(deltas)-recentK:]
	recentAvg := mean(recent)
	baselineMedian := median(baselineRaw)

	// 5. ratio (zero baseline guard per SPEC: ratio 降级为 0 走 fallback)
	if baselineMedian.IsZero() {
		return SquareHotResult{
			Mode: mode, SampleCount: n, DataSpanHours: spanHours,
			Ratio: decimal.Zero, FailedReason: "zero_baseline_median",
		}, nil
	}
	ratio := recentAvg.Div(baselineMedian)

	// 6. acceleration (skip for short mode)
	var acceleration decimal.Decimal
	if mode != ModeShort {
		accelSrc := deltas
		if len(accelSrc) > accelWindow {
			accelSrc = accelSrc[len(accelSrc)-accelWindow:]
		}
		acceleration = posRatio(secondOrderDiff(accelSrc))
	}

	// 7. Hot decision
	result := SquareHotResult{
		Mode: mode, Ratio: ratio, Acceleration: acceleration,
		SampleCount: n, DataSpanHours: spanHours,
	}
	if ratio.LessThan(ratioThreshold) {
		result.FailedReason = "low_ratio"
		return result, nil
	}
	if mode == ModeShort {
		result.Hot = true
		return result, nil
	}
	if acceleration.LessThan(accelThreshold) {
		result.FailedReason = "low_acceleration"
		return result, nil
	}
	result.Hot = true
	return result, nil
}

// --- helpers ---

// computeNPeriodDeltas returns Δᵢ = c[i+n] - c[i] for all valid i.
// n=1 → 15min δ (short mode), n=4 (default) → 60min Δ (standard/medium modes).
func computeNPeriodDeltas(c []decimal.Decimal, n int) []decimal.Decimal {
	if len(c) <= n || n < 1 {
		return nil
	}
	out := make([]decimal.Decimal, len(c)-n)
	for i := range out {
		out[i] = c[i+n].Sub(c[i])
	}
	return out
}

func secondOrderDiff(xs []decimal.Decimal) []decimal.Decimal {
	if len(xs) < 3 {
		return nil
	}
	first := make([]decimal.Decimal, len(xs)-1)
	for i := range first {
		first[i] = xs[i+1].Sub(xs[i])
	}
	second := make([]decimal.Decimal, len(first)-1)
	for i := range second {
		second[i] = first[i+1].Sub(first[i])
	}
	return second
}

func mean(xs []decimal.Decimal) decimal.Decimal {
	if len(xs) == 0 {
		return decimal.Zero
	}
	sum := decimal.Zero
	for _, x := range xs {
		sum = sum.Add(x)
	}
	return sum.Div(decimal.NewFromInt(int64(len(xs))))
}

func median(xs []decimal.Decimal) decimal.Decimal {
	if len(xs) == 0 {
		return decimal.Zero
	}
	sorted := make([]decimal.Decimal, len(xs))
	copy(sorted, xs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LessThan(sorted[j]) })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return sorted[n/2-1].Add(sorted[n/2]).Div(decimal.NewFromInt(2))
}

func posRatio(xs []decimal.Decimal) decimal.Decimal {
	if len(xs) == 0 {
		return decimal.Zero
	}
	pos := 0
	for _, x := range xs {
		if x.IsPositive() {
			pos++
		}
	}
	return decimal.NewFromInt(int64(pos)).Div(decimal.NewFromInt(int64(len(xs))))
}
