// Phase 5.2 Round 2.x: config_reloader — 1min cron that reads admin_overrides
// and atomically swaps the runtime config. Trader consumers (circuit_breaker
// for now; signal_engine + executor 后续 Round 2.y) read config.Get() each
// evaluation, so changes take effect within 1min of admin Web UI update.
//
// Failure handling: DB query fails → log + keep current runtime (fail-safe).
// Per-key parse failure → log + skip that key (fall back to baseline).

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	cfgpkg "trader/internal/config"
	"trader/internal/pkg/metrics"
)

// ConfigReloaderConfig is the cron knobs.
type ConfigReloaderConfig struct {
	PerTickTimeout time.Duration
}

func configReloaderDefaults(cfg ConfigReloaderConfig) ConfigReloaderConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 5 * time.Second
	}
	return cfg
}

// ConfigReloader is the collector.
type ConfigReloader struct {
	db       *pgxpool.Pool
	baseline *cfgpkg.Runtime // immutable, captured at startup from .env
	log      zerolog.Logger
	cfg      ConfigReloaderConfig
}

func NewConfigReloader(db *pgxpool.Pool, baseline *cfgpkg.Runtime, log zerolog.Logger, cfg ConfigReloaderConfig) *ConfigReloader {
	return &ConfigReloader{
		db:       db,
		baseline: baseline,
		log:      log,
		cfg:      configReloaderDefaults(cfg),
	}
}

func (c *ConfigReloader) Name() string { return "config_reloader" }

// Run implements collector.Collector. Reloads runtime config from admin_overrides
// table + baseline. Atomic swap — consumers never see partial state.
func (c *ConfigReloader) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	overrides, err := c.queryOverrides(tickCtx)
	if err != nil {
		c.log.Warn().Err(err).Msg("config_reloader: query failed, keeping current runtime")
		metrics.ConfigReloadTotal.WithLabelValues("err").Inc()
		return nil // don't return error — fail-safe, current runtime stays
	}

	// Build new runtime from baseline + overrides.
	newRt := *c.baseline // shallow copy is fine (decimal.Decimal is value type)
	changed := []string{}
	for key, val := range overrides {
		if c.applyOverride(&newRt, key, val) {
			changed = append(changed, key)
		}
	}

	// Detect drift from current. If no diff, skip the swap (and the noisy log).
	cur := cfgpkg.Get()
	if cur != nil && runtimesEqual(cur, &newRt) {
		metrics.ConfigReloadTotal.WithLabelValues("nochange").Inc()
		return nil
	}

	cfgpkg.Set(&newRt)
	c.log.Info().
		Strs("changed_keys", changed).
		Str("daily_loss_halt_pct", newRt.DailyLossHaltPct.String()).
		Int("consecutive_losses_halt", newRt.ConsecutiveLossesHalt).
		Msg("config_reloader.tick: runtime swapped")
	metrics.ConfigReloadTotal.WithLabelValues("ok").Inc()
	return nil
}

// queryOverrides returns the latest admin_overrides as key→value map.
// value is whatever JSONB shape admin Web UI stored (typically {"value": ...}).
func (c *ConfigReloader) queryOverrides(ctx context.Context) (map[string]any, error) {
	rows, err := c.db.Query(ctx, `SELECT key, value FROM admin_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var wrapped struct {
			Value any `json:"value"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			c.log.Warn().Err(err).Str("key", key).Msg("config_reloader: malformed value, skipping")
			continue
		}
		out[key] = wrapped.Value
	}
	return out, rows.Err()
}

// applyOverride mutates newRt with the override for `key`. Returns true if
// the value was successfully parsed + applied. Malformed values are logged
// and skipped (baseline value preserved in newRt).
func (c *ConfigReloader) applyOverride(newRt *cfgpkg.Runtime, key string, val any) bool {
	switch key {
	case "DAILY_LOSS_HALT_PCT":
		if d, ok := toDecimal(val); ok && !d.IsZero() {
			newRt.DailyLossHaltPct = d
			return true
		}
	case "CONSECUTIVE_LOSSES_HALT":
		if n, ok := toInt(val); ok && n > 0 {
			newRt.ConsecutiveLossesHalt = n
			return true
		}
	default:
		// Unknown keys are stored (admin Web UI accepts them) but not yet
		// wired into a runtime field. Round 2.y / 2.z adds more cases.
		c.log.Debug().Str("key", key).Msg("config_reloader: key not yet wired into Runtime")
	}
	return false
}

func toDecimal(v any) (decimal.Decimal, bool) {
	switch t := v.(type) {
	case string:
		d, err := decimal.NewFromString(t)
		if err != nil {
			return decimal.Zero, false
		}
		return d, true
	case float64:
		return decimal.NewFromFloat(t), true
	case int:
		return decimal.NewFromInt(int64(t)), true
	case int64:
		return decimal.NewFromInt(t), true
	}
	return decimal.Zero, false
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		// JSON numbers usually decode as float64; string fallback for safety.
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func runtimesEqual(a, b *cfgpkg.Runtime) bool {
	return a.DailyLossHaltPct.Equal(b.DailyLossHaltPct) &&
		a.ConsecutiveLossesHalt == b.ConsecutiveLossesHalt
}
