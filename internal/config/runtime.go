// Phase 5.2 Round 2.x: hot-reloadable runtime config.
//
// Background: cfg is loaded once at startup from .env. Long-running consumers
// (CircuitBreakerTripper, SignalEngine, etc.) cache values via constructor.
// admin Web UI Round 2 writes admin_overrides to DB, but trader doesn't see
// changes without a restart.
//
// Runtime is an atomic.Pointer holding the override-able subset. config_reloader
// (1min cron) queries admin_overrides, builds a new Runtime from baseline + DB
// overrides, and atomically swaps. Consumers call config.Get() each evaluation
// for fresh values — no consumer-side wiring beyond the getter swap.
//
// Initial seed: main() calls InitRuntimeFromConfig(cfg) once, populating from
// .env values. Reloader then takes over per-tick updates.
//
// Priority: admin_overrides (DB) > .env (baseline) > zero defaults (handled by config.Load).
package config

import (
	"sync/atomic"

	"github.com/shopspring/decimal"
)

// Runtime is the hot-swappable config subset. Add fields incrementally as
// admin Web UI exposes more overrides.
//
// 8 wired keys (Round 2.x Part 1 + 2.y + signal_engine refactor):
//   DAILY_LOSS_HALT_PCT       → DailyLossHaltPct        (circuit_breaker)
//   CONSECUTIVE_LOSSES_HALT   → ConsecutiveLossesHalt   (circuit_breaker)
//   TOTAL_FLOAT_LOSS_HALT_PCT → TotalFloatLossHaltPct   (circuit_breaker, R.2y)
//   BTC_PANIC_DROP_PCT        → BTCCrashHaltPct         (circuit_breaker, R.2y)
//   MAX_STOP_PCT              → MaxStopPct              (executor, R.2y)
//   LEVERAGE                  → Leverage                (executor, R.2y)
//   OI_GROWTH_FROM_MIN_PCT    → OiGrowthFromMinPct      (signal_engine, R.2y refactor)
//   SQUARE_HOT_MULTIPLIER     → SquareHotMultiplier     (signal_engine, R.2y refactor)
//
// Round 2.z (mu 真盘 owner 真实诉求): 4 trail stage thresholds added.
// Note: S1 uses "Activate" (entry-time + S0→S1 transition); S2/S3/S4 use
// "Upgrade" (existing trail tightening). Same semantic — "when pct_gain
// reaches X, advance to next stage" — but key names follow the existing
// config.Exit.* fields, not mu's spec wording.
//   TRAIL_STAGE1_ACTIVATE_PCT → TrailStage1ActivatePct  (executor + trail_upgrader)
//   TRAIL_STAGE2_UPGRADE_PCT  → TrailStage2UpgradePct   (trail_upgrader S1→S2)
//   TRAIL_STAGE3_UPGRADE_PCT  → TrailStage3UpgradePct   (trail_upgrader S2→S3)
//   TRAIL_STAGE4_UPGRADE_PCT  → TrailStage4UpgradePct   (trail_upgrader S3→S4)
type Runtime struct {
	DailyLossHaltPct      decimal.Decimal
	ConsecutiveLossesHalt int
	// Round 2.y additions:
	TotalFloatLossHaltPct decimal.Decimal
	BTCCrashHaltPct       decimal.Decimal
	MaxStopPct            decimal.Decimal
	Leverage              int
	// signal_engine refactor (Round 2.y final 2 keys):
	OiGrowthFromMinPct  decimal.Decimal
	SquareHotMultiplier decimal.Decimal
	// Round 2.z trail thresholds (mu 真盘 owner catch — trail S1 +3% 太低):
	TrailStage1ActivatePct decimal.Decimal
	TrailStage2UpgradePct  decimal.Decimal
	TrailStage3UpgradePct  decimal.Decimal
	TrailStage4UpgradePct  decimal.Decimal
	// Round 2.w trail callback rates (mu 真盘 owner 2026-05-14 catch — 之前
	// Round 2.z 只 wire 了 activate/upgrade,callback 改 .env 不生效):
	TrailStage1CallbackRate decimal.Decimal // S1: ≤ 0.05 (Binance native upper bound)
	TrailStage2CallbackRate decimal.Decimal // S2: ≤ 0.05 (Binance native upper bound)
	TrailStage3CallbackRate decimal.Decimal // S3: trader-managed, no Binance limit
	TrailStage4CallbackRate decimal.Decimal // S4: trader-managed, no Binance limit
	// Round R.7 F2: API error rate halt threshold (mu 真盘 13:30 BJT 2026-05-14
	// catch — 代理 13 秒认证故障 → 28 errors/min trip → 24h false halt).
	// Old default 3/min too sensitive for proxy transient spikes. Wired to
	// admin Web UI for mu to tune live as proxy provider stability improves.
	APIErrorRateLimit int
}

var runtime atomic.Pointer[Runtime]

// Get returns the current runtime config. Nil means InitRuntimeFromConfig
// wasn't called — callers should fall back to their cached cfg values.
func Get() *Runtime {
	return runtime.Load()
}

// Set replaces the runtime atomically. Called by config_reloader after a
// successful admin_overrides query. Production code should prefer
// InitRuntimeFromConfig + reloader; this is exposed mainly for tests.
func Set(r *Runtime) {
	runtime.Store(r)
}

// InitRuntimeFromConfig seeds the runtime from .env-loaded Config. Called once
// at trader startup, BEFORE collectors register. Subsequent reloads overlay
// admin_overrides on top of these baselines.
func InitRuntimeFromConfig(cfg *Config) {
	Set(&Runtime{
		DailyLossHaltPct:      cfg.Risk.DailyLossHaltPct,
		ConsecutiveLossesHalt: cfg.Risk.ConsecutiveLossHaltCount,
		TotalFloatLossHaltPct: cfg.Risk.TotalFloatLossHaltPct,
		BTCCrashHaltPct:       cfg.Risk.BTCCrashHaltPct,
		MaxStopPct:            cfg.Exit.MaxStopPct,
		Leverage:              cfg.Position.Leverage,
		OiGrowthFromMinPct:    cfg.OISurge.FromLowPct,
		SquareHotMultiplier:   cfg.SquareHot.Multiplier,
		// Round 2.z trail thresholds:
		TrailStage1ActivatePct: cfg.Exit.TrailStage1ActivatePct,
		TrailStage2UpgradePct:  cfg.Exit.TrailStage2UpgradePct,
		TrailStage3UpgradePct:  cfg.Exit.TrailStage3UpgradePct,
		TrailStage4UpgradePct:  cfg.Exit.TrailStage4UpgradePct,
		// Round 2.w trail callback rates:
		TrailStage1CallbackRate: cfg.Exit.TrailStage1CallbackRate,
		TrailStage2CallbackRate: cfg.Exit.TrailStage2CallbackRate,
		TrailStage3CallbackRate: cfg.Exit.TrailStage3CallbackRate,
		TrailStage4CallbackRate: cfg.Exit.TrailStage4CallbackRate,
		// Round R.7 F2:
		APIErrorRateLimit: cfg.Risk.APIErrorRateLimit,
	})
}
