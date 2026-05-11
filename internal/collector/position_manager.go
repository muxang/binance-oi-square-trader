// Phase 4 Round 3: position_manager cron — 1min sync of open positions.
//
// Wraps execution.PositionManager.SyncTick in the standard collector contract.
// Failure paths internalized; errors logged + metrics, never bubble to runner
// (other collectors must keep running).

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type PositionManagerConfig struct {
	PerTickTimeout time.Duration
}

func positionManagerDefaults(cfg PositionManagerConfig) PositionManagerConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 30 * time.Second
	}
	return cfg
}

// PositionManagerCollector is the cron-runner adapter for position sync.
type PositionManagerCollector struct {
	pm  *execution.PositionManager
	log zerolog.Logger
	cfg PositionManagerConfig
}

// NewPositionManagerCollector wires the cron-side adapter.
func NewPositionManagerCollector(pm *execution.PositionManager, log zerolog.Logger, cfg PositionManagerConfig) *PositionManagerCollector {
	cfg = positionManagerDefaults(cfg)
	return &PositionManagerCollector{pm: pm, log: log, cfg: cfg}
}

func (c *PositionManagerCollector) Name() string { return "position_manager" }

func (c *PositionManagerCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.pm.SyncTick(tickCtx)
	return nil
}
