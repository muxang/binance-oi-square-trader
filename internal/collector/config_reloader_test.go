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
