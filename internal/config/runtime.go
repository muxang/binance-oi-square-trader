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
// Round 2.x Part 1 wired 2 keys; Round 2.y adds 4 more (6 wired total):
//   DAILY_LOSS_HALT_PCT       → DailyLossHaltPct        (circuit_breaker)
//   CONSECUTIVE_LOSSES_HALT   → ConsecutiveLossesHalt   (circuit_breaker)
//   TOTAL_FLOAT_LOSS_HALT_PCT → TotalFloatLossHaltPct   (circuit_breaker, R.2y)
//   BTC_PANIC_DROP_PCT        → BTCCrashHaltPct         (circuit_breaker, R.2y)
//   MAX_STOP_PCT              → MaxStopPct              (executor, R.2y)
//   LEVERAGE                  → Leverage                (executor, R.2y)
//
// Deferred (signal_engine refactor too invasive for current scope):
//   OI_SURGE_MIN_GROWING_RATIO, SQUARE_HOT_MULTIPLIER
type Runtime struct {
	DailyLossHaltPct      decimal.Decimal
	ConsecutiveLossesHalt int
	// Round 2.y additions:
	TotalFloatLossHaltPct decimal.Decimal
	BTCCrashHaltPct       decimal.Decimal
	MaxStopPct            decimal.Decimal
	Leverage              int
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
	})
}
