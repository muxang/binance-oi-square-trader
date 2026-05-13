// Unit tests for Executor.computeStopPct — ATR-based disaster stop formula.
// Uses miniredis (no real Redis); no Binance calls needed.
package execution

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfgpkg "trader/internal/config"
)

func newTestExecutor(t *testing.T, mr *miniredis.Miniredis) *Executor {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Executor{
		rdb: rdb,
		cfg: Config{
			DisasterStopPct: decimal.NewFromFloat(0.06),
			ATRStopMult:     decimal.NewFromFloat(2.0),
			MinStopPct:      decimal.NewFromFloat(0.06),
			// Round R.1 (mu 2026-05-13): MAX 7.5% → 12% (5x leverage 适配高波动).
			MaxStopPct: decimal.NewFromFloat(0.12),
		},
		log: zerolog.Nop(),
	}
}

func setATR(t *testing.T, mr *miniredis.Miniredis, symbol, value string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"value": value, "computed_at": "2026-05-12T00:00:00Z"})
	require.NoError(t, mr.Set("atr:"+symbol, string(payload)))
}

// BTC-class: ATR/price ~1% → ATR×2=2% → clips to MIN=6%
func TestComputeStopPct_BTCClass_ClipsToMin(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "BTCUSDT", "800") // ATR=800, price=80000 → 1% → ×2=2% < MIN
	pct := e.computeStopPct(context.Background(), "BTCUSDT", decimal.NewFromFloat(80000), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.06)), "BTC: expect MIN=6%%, got %s", pct)
}

// Mid-coin: ATR/price ~2.5% → ATR×2=5% → clips to MIN=6%
func TestComputeStopPct_MidCoin_ClipsToMin(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "SOLUSDT", "3.75") // ATR=3.75, price=150 → 2.5% → ×2=5% < MIN
	pct := e.computeStopPct(context.Background(), "SOLUSDT", decimal.NewFromFloat(150), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.06)), "mid-coin: expect MIN=6%%, got %s", pct)
}

// Alt-coin (RIFUSDT-class): ATR/price ~3.5% → ATR×2=7% → in [6%, 12%]
func TestComputeStopPct_AltCoin_ATRBased(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "RIFUSDT", "0.007") // ATR=0.007, price=0.2 → 3.5% → ×2=7%
	pct := e.computeStopPct(context.Background(), "RIFUSDT", decimal.NewFromFloat(0.2), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.07)), "alt-coin: expect 7%%, got %s", pct)
}

// Mid-volatility alt: ATR/price ~5% → ATR×2=10% → in [6%, 12%] post Round R.1
func TestComputeStopPct_MidAlt_ATRBased(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "MIDALT", "0.05") // ATR=0.05, price=1.0 → 5% → ×2=10%
	pct := e.computeStopPct(context.Background(), "MIDALT", decimal.NewFromFloat(1.0), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.10)), "mid-alt: expect 10%%, got %s", pct)
}

// Extreme small-cap: ATR/price >6% → ATR×2 > MAX 12% → clips to MAX=12%
func TestComputeStopPct_ExtremeAlt_ClipsToMax(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "MEMECOIN", "0.05") // ATR=0.05, price=0.5 → 10% → ×2=20% > MAX 12%
	pct := e.computeStopPct(context.Background(), "MEMECOIN", decimal.NewFromFloat(0.5), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.12)), "extreme: expect MAX=12%%, got %s", pct)
}

// ATR missing from Redis → fallback to DisasterStopPct
func TestComputeStopPct_ATRMiss_Fallback(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	// no setATR call → key absent
	pct := e.computeStopPct(context.Background(), "NEWCOIN", decimal.NewFromFloat(1.0), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.06)), "ATR miss: expect fallback 6%%, got %s", pct)
}

// ATR=0 in Redis → fallback (defensive: avoid div-by-zero or garbage stop)
func TestComputeStopPct_ATRZero_Fallback(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	setATR(t, mr, "ODDCOIN", "0")
	pct := e.computeStopPct(context.Background(), "ODDCOIN", decimal.NewFromFloat(1.0), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.06)), "ATR=0: expect fallback 6%%, got %s", pct)
}

// Boundary: ATR/price × mult exactly at MIN → no clip needed
func TestComputeStopPct_ExactlyMin_NoClip(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	// ATR=3, price=100 → 3% → ×2=6% == MIN (no clip)
	setATR(t, mr, "EXACTMIN", "3")
	pct := e.computeStopPct(context.Background(), "EXACTMIN", decimal.NewFromFloat(100), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.06)), "exactly MIN: got %s", pct)
}

// Boundary: ATR/price × mult exactly at MAX 12% → clips to MAX
func TestComputeStopPct_ExactlyMax_NoExtraClip(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr)
	// ATR=6, price=100 → 6% → ×2=12% == MAX (no extra clip)
	setATR(t, mr, "EXACTMAX", "6")
	pct := e.computeStopPct(context.Background(), "EXACTMAX", decimal.NewFromFloat(100), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.12)), "exactly MAX: got %s", pct)
}

// Round 2.y: runtime override on MAX_STOP_PCT must clip to the runtime value,
// not the startup cfg value. Lets mu tighten/loosen MAX_STOP via admin Web UI
// without a restart.
func TestComputeStopPct_RuntimeOverride_ClipsToRuntimeMax(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	e := newTestExecutor(t, mr) // cfg MaxStopPct = 0.12
	// ATR=6, price=100 → 6% → ×2=12% would normally clip to 12%.
	setATR(t, mr, "RUNTIMEMAX", "6")

	// Override runtime to a tighter 0.08 — should clip earlier.
	cfgpkg.Set(&cfgpkg.Runtime{MaxStopPct: decimal.NewFromFloat(0.08)})
	defer cfgpkg.Set(nil)

	pct := e.computeStopPct(context.Background(), "RUNTIMEMAX", decimal.NewFromFloat(100), zerolog.Nop())
	assert.True(t, pct.Equal(decimal.NewFromFloat(0.08)),
		"runtime MAX_STOP_PCT=0.08 should override cfg 0.12; got %s", pct)
}

// Round 2.y: leverage helper falls back to cfg when runtime is unset/zero.
func TestExecutor_Leverage_RuntimeOverride(t *testing.T) {
	e := &Executor{cfg: Config{Leverage: 10}}
	cfgpkg.Set(nil) // clean slate
	assert.Equal(t, 10, e.leverage(), "no runtime → cfg fallback")

	cfgpkg.Set(&cfgpkg.Runtime{Leverage: 5})
	defer cfgpkg.Set(nil)
	assert.Equal(t, 5, e.leverage(), "runtime override wins")

	cfgpkg.Set(&cfgpkg.Runtime{Leverage: 0})
	assert.Equal(t, 10, e.leverage(), "zero runtime → cfg fallback")
}

// Round 2.z: trail S1 activation threshold — admin Web UI override tightens or
// loosens entry-time PlaceAlgoTrailingStop activation price.
func TestExecutor_TrailStage1Activate_RuntimeOverride(t *testing.T) {
	e := &Executor{cfg: Config{TrailStage1ActivatePct: decimal.NewFromFloat(0.03)}}
	cfgpkg.Set(nil)
	assert.True(t, e.trailStage1ActivatePct().Equal(decimal.NewFromFloat(0.03)), "no runtime → cfg fallback")

	cfgpkg.Set(&cfgpkg.Runtime{TrailStage1ActivatePct: decimal.NewFromFloat(0.05)})
	defer cfgpkg.Set(nil)
	assert.True(t, e.trailStage1ActivatePct().Equal(decimal.NewFromFloat(0.05)),
		"runtime override = 0.05 wins over cfg 0.03 (mu 真盘 owner 真实诉求 catch)")

	cfgpkg.Set(&cfgpkg.Runtime{}) // zero runtime
	assert.True(t, e.trailStage1ActivatePct().Equal(decimal.NewFromFloat(0.03)), "zero runtime → cfg fallback")
}
