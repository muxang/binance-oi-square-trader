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
// Phase 5.2 Round 2.x wires 2 keys (most-impactful for mu RCA-driven tuning):
//   DAILY_LOSS_HALT_PCT       → DailyLossHaltPct
//   CONSECUTIVE_LOSSES_HALT   → ConsecutiveLossesHalt
//
// Future keys (TODO):
//   TOTAL_FLOAT_LOSS_HALT_PCT, BTC_PANIC_DROP_PCT, OI_GROWTH_FROM_MIN_PCT,
//   SQUARE_HOT_MULTIPLIER, MAX_STOP_PCT, LEVERAGE
type Runtime struct {
	DailyLossHaltPct      decimal.Decimal
	ConsecutiveLossesHalt int
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
	})
}
