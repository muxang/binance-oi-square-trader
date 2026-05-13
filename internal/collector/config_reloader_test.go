// Phase 5.2 Round 2.x: config_reloader unit tests focused on parse + override
// merge logic. DB integration (queryOverrides) is exercised in deploy verify.
package collector

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	cfgpkg "trader/internal/config"
)

func TestApplyOverride_DailyLossHaltPct_String(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{
		DailyLossHaltPct:      decimal.NewFromFloat(0.08),
		ConsecutiveLossesHalt: 8,
	}
	ok := c.applyOverride(rt, "DAILY_LOSS_HALT_PCT", "0.06")
	assert.True(t, ok)
	assert.True(t, rt.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.06)))
}

func TestApplyOverride_DailyLossHaltPct_Float(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{}
	ok := c.applyOverride(rt, "DAILY_LOSS_HALT_PCT", 0.05)
	assert.True(t, ok)
	assert.True(t, rt.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.05)))
}

func TestApplyOverride_DailyLossHaltPct_Zero_Skipped(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.08)}
	ok := c.applyOverride(rt, "DAILY_LOSS_HALT_PCT", "0")
	assert.False(t, ok)
	// baseline preserved
	assert.True(t, rt.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.08)))
}

func TestApplyOverride_DailyLossHaltPct_Malformed_Skipped(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.08)}
	ok := c.applyOverride(rt, "DAILY_LOSS_HALT_PCT", "abc")
	assert.False(t, ok)
	assert.True(t, rt.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.08)))
}

func TestApplyOverride_ConsecutiveLossesHalt_Float(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{ConsecutiveLossesHalt: 8}
	ok := c.applyOverride(rt, "CONSECUTIVE_LOSSES_HALT", float64(5))
	assert.True(t, ok)
	assert.Equal(t, 5, rt.ConsecutiveLossesHalt)
}

func TestApplyOverride_ConsecutiveLossesHalt_Negative_Skipped(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{ConsecutiveLossesHalt: 8}
	ok := c.applyOverride(rt, "CONSECUTIVE_LOSSES_HALT", -1)
	assert.False(t, ok)
	assert.Equal(t, 8, rt.ConsecutiveLossesHalt)
}

func TestApplyOverride_UnknownKey_Logged(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.08)}
	ok := c.applyOverride(rt, "FUTURE_KEY", "value")
	assert.False(t, ok, "unknown key returns false (not wired yet)")
	assert.True(t, rt.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.08)))
}

func TestRuntimesEqual(t *testing.T) {
	a := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.06), ConsecutiveLossesHalt: 8}
	b := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.06), ConsecutiveLossesHalt: 8}
	c := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.05), ConsecutiveLossesHalt: 8}
	assert.True(t, runtimesEqual(a, b))
	assert.False(t, runtimesEqual(a, c))

	// Round 2.y: equality must also key off the 4 new fields, otherwise
	// no-change detection misfires and the reloader logs a swap every tick.
	d := &cfgpkg.Runtime{TotalFloatLossHaltPct: decimal.NewFromFloat(0.12)}
	e := &cfgpkg.Runtime{TotalFloatLossHaltPct: decimal.NewFromFloat(0.10)}
	assert.False(t, runtimesEqual(d, e))
	f := &cfgpkg.Runtime{BTCCrashHaltPct: decimal.NewFromFloat(0.03)}
	g := &cfgpkg.Runtime{BTCCrashHaltPct: decimal.NewFromFloat(0.05)}
	assert.False(t, runtimesEqual(f, g))
	h := &cfgpkg.Runtime{MaxStopPct: decimal.NewFromFloat(0.12)}
	i := &cfgpkg.Runtime{MaxStopPct: decimal.NewFromFloat(0.10)}
	assert.False(t, runtimesEqual(h, i))
	j := &cfgpkg.Runtime{Leverage: 5}
	k := &cfgpkg.Runtime{Leverage: 10}
	assert.False(t, runtimesEqual(j, k))
}

// Round 2.y: 4 new wire-up keys (TOTAL_FLOAT / BTC_PANIC / MAX_STOP / LEVERAGE).
func TestApplyOverride_TotalFloatLossHaltPct(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TotalFloatLossHaltPct: decimal.NewFromFloat(0.12)}
	ok := c.applyOverride(rt, "TOTAL_FLOAT_LOSS_HALT_PCT", "0.10")
	assert.True(t, ok)
	assert.True(t, rt.TotalFloatLossHaltPct.Equal(decimal.NewFromFloat(0.10)))
}

func TestApplyOverride_BTCPanicDropPct(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{BTCCrashHaltPct: decimal.NewFromFloat(0.03)}
	ok := c.applyOverride(rt, "BTC_PANIC_DROP_PCT", "0.05")
	assert.True(t, ok)
	assert.True(t, rt.BTCCrashHaltPct.Equal(decimal.NewFromFloat(0.05)))
}

func TestApplyOverride_MaxStopPct(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{MaxStopPct: decimal.NewFromFloat(0.12)}
	ok := c.applyOverride(rt, "MAX_STOP_PCT", "0.10")
	assert.True(t, ok)
	assert.True(t, rt.MaxStopPct.Equal(decimal.NewFromFloat(0.10)))
}

func TestApplyOverride_Leverage(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{Leverage: 10}
	ok := c.applyOverride(rt, "LEVERAGE", float64(5))
	assert.True(t, ok)
	assert.Equal(t, 5, rt.Leverage)
}

func TestApplyOverride_Leverage_OutOfRange(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{Leverage: 10}
	ok := c.applyOverride(rt, "LEVERAGE", float64(200)) // > 125 (Binance max)
	assert.False(t, ok)
	assert.Equal(t, 10, rt.Leverage, "out-of-range value skipped, baseline preserved")
}

// signal_engine refactor: final 2 wired keys (Round 2.y).
func TestApplyOverride_OiGrowthFromMinPct(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{OiGrowthFromMinPct: decimal.NewFromFloat(0.05)}
	ok := c.applyOverride(rt, "OI_GROWTH_FROM_MIN_PCT", "0.06")
	assert.True(t, ok)
	assert.True(t, rt.OiGrowthFromMinPct.Equal(decimal.NewFromFloat(0.06)))
}

func TestApplyOverride_OiGrowthFromMinPct_Zero_Skipped(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{OiGrowthFromMinPct: decimal.NewFromFloat(0.05)}
	ok := c.applyOverride(rt, "OI_GROWTH_FROM_MIN_PCT", "0")
	assert.False(t, ok)
	assert.True(t, rt.OiGrowthFromMinPct.Equal(decimal.NewFromFloat(0.05)))
}

func TestApplyOverride_SquareHotMultiplier(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{SquareHotMultiplier: decimal.NewFromFloat(2.0)}
	ok := c.applyOverride(rt, "SQUARE_HOT_MULTIPLIER", "2.5")
	assert.True(t, ok)
	assert.True(t, rt.SquareHotMultiplier.Equal(decimal.NewFromFloat(2.5)))
}

func TestRuntimesEqual_SignalKeys(t *testing.T) {
	a := &cfgpkg.Runtime{OiGrowthFromMinPct: decimal.NewFromFloat(0.06)}
	b := &cfgpkg.Runtime{OiGrowthFromMinPct: decimal.NewFromFloat(0.05)}
	assert.False(t, runtimesEqual(a, b))
	c := &cfgpkg.Runtime{SquareHotMultiplier: decimal.NewFromFloat(2.0)}
	d := &cfgpkg.Runtime{SquareHotMultiplier: decimal.NewFromFloat(2.5)}
	assert.False(t, runtimesEqual(c, d))
}

// Round 2.z trail stage thresholds (mu 真盘 owner catch — trail S1 +3% 太低).
func TestApplyOverride_TrailStage1Activate(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage1ActivatePct: decimal.NewFromFloat(0.03)}
	ok := c.applyOverride(rt, "TRAIL_STAGE1_ACTIVATE_PCT", "0.05")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage1ActivatePct.Equal(decimal.NewFromFloat(0.05)))
}

func TestApplyOverride_TrailStage2Upgrade(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage2UpgradePct: decimal.NewFromFloat(0.15)}
	ok := c.applyOverride(rt, "TRAIL_STAGE2_UPGRADE_PCT", "0.20")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage2UpgradePct.Equal(decimal.NewFromFloat(0.20)))
}

func TestApplyOverride_TrailStage3Upgrade(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage3UpgradePct: decimal.NewFromFloat(0.30)}
	ok := c.applyOverride(rt, "TRAIL_STAGE3_UPGRADE_PCT", "0.35")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage3UpgradePct.Equal(decimal.NewFromFloat(0.35)))
}

func TestApplyOverride_TrailStage4Upgrade(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage4UpgradePct: decimal.NewFromFloat(0.60)}
	ok := c.applyOverride(rt, "TRAIL_STAGE4_UPGRADE_PCT", "0.65")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage4UpgradePct.Equal(decimal.NewFromFloat(0.65)))
}

func TestApplyOverride_TrailStage_OutOfRange_Skipped(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage1ActivatePct: decimal.NewFromFloat(0.05)}
	// 1.5 = 150%, > 1 → skipped, baseline preserved
	ok := c.applyOverride(rt, "TRAIL_STAGE1_ACTIVATE_PCT", "1.5")
	assert.False(t, ok)
	assert.True(t, rt.TrailStage1ActivatePct.Equal(decimal.NewFromFloat(0.05)))
}

func TestRuntimesEqual_TrailKeys(t *testing.T) {
	a := &cfgpkg.Runtime{TrailStage1ActivatePct: decimal.NewFromFloat(0.05)}
	b := &cfgpkg.Runtime{TrailStage1ActivatePct: decimal.NewFromFloat(0.03)}
	assert.False(t, runtimesEqual(a, b))
	c := &cfgpkg.Runtime{TrailStage4UpgradePct: decimal.NewFromFloat(0.65)}
	d := &cfgpkg.Runtime{TrailStage4UpgradePct: decimal.NewFromFloat(0.60)}
	assert.False(t, runtimesEqual(c, d))
}

// Round 2.w trail callback rates — same pattern as Round 2.z activate/upgrade.
func TestApplyOverride_TrailStage1CallbackRate(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage1CallbackRate: decimal.NewFromFloat(0.03)}
	ok := c.applyOverride(rt, "TRAIL_STAGE1_CALLBACK_RATE", "0.04")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage1CallbackRate.Equal(decimal.NewFromFloat(0.04)))
}

func TestApplyOverride_TrailStage2CallbackRate(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage2CallbackRate: decimal.NewFromFloat(0.05)}
	ok := c.applyOverride(rt, "TRAIL_STAGE2_CALLBACK_RATE", "0.04")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage2CallbackRate.Equal(decimal.NewFromFloat(0.04)))
}

func TestApplyOverride_TrailStage3CallbackRate(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage3CallbackRate: decimal.NewFromFloat(0.10)}
	ok := c.applyOverride(rt, "TRAIL_STAGE3_CALLBACK_RATE", "0.12")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage3CallbackRate.Equal(decimal.NewFromFloat(0.12)))
}

func TestApplyOverride_TrailStage4CallbackRate(t *testing.T) {
	c := &ConfigReloader{}
	rt := &cfgpkg.Runtime{TrailStage4CallbackRate: decimal.NewFromFloat(0.15)}
	ok := c.applyOverride(rt, "TRAIL_STAGE4_CALLBACK_RATE", "0.20")
	assert.True(t, ok)
	assert.True(t, rt.TrailStage4CallbackRate.Equal(decimal.NewFromFloat(0.20)))
}

func TestRuntimesEqual_TrailCallbackKeys(t *testing.T) {
	a := &cfgpkg.Runtime{TrailStage1CallbackRate: decimal.NewFromFloat(0.03)}
	b := &cfgpkg.Runtime{TrailStage1CallbackRate: decimal.NewFromFloat(0.04)}
	assert.False(t, runtimesEqual(a, b))
	c := &cfgpkg.Runtime{TrailStage4CallbackRate: decimal.NewFromFloat(0.15)}
	d := &cfgpkg.Runtime{TrailStage4CallbackRate: decimal.NewFromFloat(0.20)}
	assert.False(t, runtimesEqual(c, d))
}

func TestRuntime_GetSet_Atomic(t *testing.T) {
	r1 := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.06)}
	cfgpkg.Set(r1)
	got := cfgpkg.Get()
	assert.True(t, got.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.06)))

	r2 := &cfgpkg.Runtime{DailyLossHaltPct: decimal.NewFromFloat(0.04)}
	cfgpkg.Set(r2)
	got = cfgpkg.Get()
	assert.True(t, got.DailyLossHaltPct.Equal(decimal.NewFromFloat(0.04)))
}
