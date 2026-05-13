// v0.2 Round 1 Module B: trail_upgrader cron — 5min sweep that activates,
// upgrades, and ratchets the 4-stage trailing stop per open trade.
// Wraps execution.TrailUpgrader.ReconcileTick in the collector contract.

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type TrailUpgraderConfig struct {
	PerTickTimeout time.Duration
}

func trailUpgraderDefaults(cfg TrailUpgraderConfig) TrailUpgraderConfig {
	if cfg.PerTickTimeout == 0 {
		// Each row may: 1× GET latest_price + 1× Cancel + 1× Place + 1-2× DB.
		// 20 open trades worst case → 60s budget covers slow proxy.
		cfg.PerTickTimeout = 60 * time.Second
	}
	return cfg
}

type TrailUpgraderCollector struct {
	tu  *execution.TrailUpgrader
	log zerolog.Logger
	cfg TrailUpgraderConfig
}

func NewTrailUpgraderCollector(tu *execution.TrailUpgrader, log zerolog.Logger, cfg TrailUpgraderConfig) *TrailUpgraderCollector {
	cfg = trailUpgraderDefaults(cfg)
	return &TrailUpgraderCollector{tu: tu, log: log, cfg: cfg}
}

func (c *TrailUpgraderCollector) Name() string { return "trail_upgrader" }

func (c *TrailUpgraderCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.tu.ReconcileTick(tickCtx)
	return nil
}
