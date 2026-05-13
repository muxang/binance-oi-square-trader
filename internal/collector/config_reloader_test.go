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
