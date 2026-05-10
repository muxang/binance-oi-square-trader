package signal

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// d / dseries reused from oi_surge_test.go (same package).

// fillN returns a slice of n decimals all equal to v.
func fillN(v float64, n int) []decimal.Decimal {
	out := make([]decimal.Decimal, n)
	dv := d(v)
	for i := range out {
		out[i] = dv
	}
	return out
}

// linearGrowth returns n samples starting at start, each += step.
func linearGrowth(start, step float64, n int) []decimal.Decimal {
	out := make([]decimal.Decimal, n)
	for i := range out {
		out[i] = decimal.NewFromFloat(start + float64(i)*step)
	}
	return out
}

// concatDS concatenates decimal slices.
func concatDS(parts ...[]decimal.Decimal) []decimal.Decimal {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]decimal.Decimal, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// --- 4 mode tests ---

func TestSquareHot_StandardMode_Burst_Hot(t *testing.T) {
	// 24h slow growth then explosive last 7 samples → ratio + accel both high
	baseline := linearGrowth(100, 1.25, 90) // 90 samples × 15min, slow growth
	last := baseline[len(baseline)-1]
	burst := []decimal.Decimal{
		last.Add(d(20)), last.Add(d(50)), last.Add(d(100)),
		last.Add(d(200)), last.Add(d(400)), last.Add(d(700)), last.Add(d(1100)),
	}
	cc := concatDS(baseline, burst) // n=97 → span=24h → Standard
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeStandard, r.Mode)
	assert.True(t, r.Hot, "exponential burst → hot=true")
	assert.True(t, r.Ratio.GreaterThan(d(2)), "ratio=%s should exceed 2.0", r.Ratio)
	assert.True(t, r.Acceleration.GreaterThanOrEqual(d(0.6)))
	assert.Empty(t, r.FailedReason)
}

func TestSquareHot_StandardMode_Linear_FailReasonLowRatio(t *testing.T) {
	// Pure linear growth → all 60min Δ identical → ratio≈1 < 2
	cc := linearGrowth(100, 2, 97)
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeStandard, r.Mode)
	assert.False(t, r.Hot)
	assert.Equal(t, "low_ratio", r.FailedReason, "linear growth uniform Δ → ratio≈1")
}

func TestSquareHot_StandardMode_RatioOK_AccelLow_FailReasonLowAccel(t *testing.T) {
	// Baseline Δ=4 then sudden step to Δ=16 sustained — ratio=4 ✓, but
	// last 6 Δ = [4,16,16,16,16,16], Δ' = [12,0,0,0,0], Δ'' = [-12,0,0,0]
	// posRatio = 0/4 = 0 < 0.6 → low_acceleration
	baseline := linearGrowth(100, 1, 90) // Δ60min = 4 over 4 periods
	last := baseline[len(baseline)-1]
	step := []decimal.Decimal{last.Add(d(4)), last.Add(d(8)), last.Add(d(12)), last.Add(d(16))}
	postStep := []decimal.Decimal{
		step[3].Add(d(4)), step[3].Add(d(8)), step[3].Add(d(12)),
	}
	cc := concatDS(baseline, step, postStep) // n=90+4+3=97
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeStandard, r.Mode)
	assert.False(t, r.Hot)
	assert.True(t, r.Ratio.GreaterThan(d(2)), "ratio=%s should exceed 2.0", r.Ratio)
	assert.Equal(t, "low_acceleration", r.FailedReason)
	assert.True(t, r.Acceleration.LessThan(d(0.6)))
}

func TestSquareHot_MediumMode_Burst_Hot(t *testing.T) {
	// 12h baseline slow + last 5 burst → n=49 → span=12h → Medium
	baseline := linearGrowth(100, 1, 44)
	last := baseline[len(baseline)-1]
	burst := []decimal.Decimal{
		last.Add(d(50)), last.Add(d(150)), last.Add(d(350)), last.Add(d(700)), last.Add(d(1200)),
	}
	cc := concatDS(baseline, burst) // n=49
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeMedium, r.Mode)
	assert.True(t, r.Hot)
}

func TestSquareHot_ShortMode_Burst_Hot(t *testing.T) {
	// 2.75h: n=12, baseline slow growth + recent burst, ratio_threshold=2.5
	cc := concatDS(
		linearGrowth(100, 1, 6), // Δ15min = 1
		dseries(120, 200, 350, 600, 950, 1400),
	) // n=12 → span=11×15min=2.75h → Short
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeShort, r.Mode)
	assert.True(t, r.Hot, "burst ratio >> 2.5 short threshold")
	assert.True(t, r.Acceleration.IsZero(), "short mode skips accel")
}

// --- 2 boundary tests (mode switch) ---

func TestSquareHot_BoundaryAt6h_PicksMedium(t *testing.T) {
	// n=25 → span = 24×15min = 6h exactly → spanHours.LessThan(6)=false → Medium
	cc := linearGrowth(100, 5, 25)
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeMedium, r.Mode, "span=6h boundary → medium")
}

func TestSquareHot_BoundaryAt24h_PicksStandard(t *testing.T) {
	// n=97 → span = 96×15min = 24h exactly → spanHours.LessThan(24)=false → Standard
	cc := linearGrowth(100, 5, 97)
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeStandard, r.Mode, "span=24h boundary → standard")
}

// --- 3 edge tests ---

func TestSquareHot_InsufficientSamples_Fallback(t *testing.T) {
	cc := linearGrowth(100, 1, 5) // 5 < MinDataPoints=8
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeFallback, r.Mode)
	assert.False(t, r.Hot)
	assert.Contains(t, r.FailedReason, "insufficient_samples")
}

func TestSquareHot_EmptyInput_Fallback(t *testing.T) {
	r, err := SquareHot(nil, SquareHotConfig{})
	require.NoError(t, err)
	assert.Equal(t, ModeFallback, r.Mode)
	assert.Equal(t, 0, r.SampleCount)
	assert.True(t, r.DataSpanHours.IsZero(), "spanHours clamps to 0 for empty input")
}

func TestSquareHot_ZeroBaselineMedian_NoCrash(t *testing.T) {
	// Flat plateau then late spike — baseline 60min Δ all 0, median=0 → fallback ratio
	cc := concatDS(
		fillN(100, 90),
		dseries(150, 250, 400, 600, 900, 1300, 1800),
	) // n=97, baseline Δ60min on flat = 0
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	assert.False(t, r.Hot)
	assert.Equal(t, "zero_baseline_median", r.FailedReason, "median=0 must short-circuit per SPEC")
	assert.True(t, r.Ratio.IsZero())
}

// --- 1 cfg test ---

func TestSquareHot_NegativeThreshold_ReturnsError(t *testing.T) {
	cfg := SquareHotConfig{StandardRatioThreshold: decimal.NewFromFloat(-1)}
	cc := linearGrowth(100, 1, 97)
	_, err := SquareHot(cc, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "thresholds must be >= 0")
}

// --- 2 added: defensive downgrade + medium accel quantization ---

func TestSquareHot_DowngradeStandardToMedium_WhenDeltas60Insufficient(t *testing.T) {
	// SamplePeriod=6h: span=24h needs n=5 (4 intervals × 6h = 24h).
	// MinDataPoints=4 to bypass fallback. n=5 → span=24h → mode=Standard.
	// periodsIn60Min = int(1h/6h) = 0 → clamped to 1 → deltas60Count = 5-1=4.
	// 4 < 6 → downgrade to Medium. 4 ≥ 4 → no further downgrade.
	cfg := SquareHotConfig{
		MinDataPoints: 4,
		SamplePeriod:  6 * time.Hour,
	}
	cc := linearGrowth(100, 5, 5) // n=5, Δ each step = 5
	r, err := SquareHot(cc, cfg)
	require.NoError(t, err)
	assert.Equal(t, ModeMedium, r.Mode, "Standard span but Δ insufficient → downgrade to Medium")
	assert.Equal(t, 5, r.SampleCount)
	assert.True(t, r.DataSpanHours.GreaterThanOrEqual(d(24)))
}

func TestSquareHot_MediumMode_AccelQuantization(t *testing.T) {
	// SPEC §辅助信号 documented quantization: medium mode accel window=4 Δ → 2 Δ''
	// → pos_ratio ∈ {0, 0.5, 1.0}. Threshold 0.6 → effectively requires pos_ratio=1.
	// This test verifies the helper math (acceleration field always quantized to 0/0.5/1).
	cc := concatDS(linearGrowth(100, 1, 44), dseries(200, 300, 500, 800, 1200))
	r, err := SquareHot(cc, SquareHotConfig{})
	require.NoError(t, err)
	require.Equal(t, ModeMedium, r.Mode)
	accel := r.Acceleration
	allowed := []decimal.Decimal{decimal.Zero, decimal.NewFromFloat(0.5), decimal.NewFromInt(1)}
	matched := false
	for _, v := range allowed {
		if accel.Equal(v) {
			matched = true
			break
		}
	}
	assert.True(t, matched, "medium accel must be 0 / 0.5 / 1, got %s", accel)
}
