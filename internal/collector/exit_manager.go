// Phase 4 Round 5: exit_manager cron — 1min sync of open positions for
// time-based exit (soft/hard timeout). Wraps execution.ExitManager.EvaluateTick
// in the standard collector contract.
//
// Failure paths internalized; errors logged + metrics, never bubble to runner
// (other collectors must keep running).

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type ExitManagerConfig struct {
	PerTickTimeout time.Duration
}

func exitManagerDefaults(cfg ExitManagerConfig) ExitManagerConfig {
	if cfg.PerTickTimeout == 0 {
		// Generous — close pipeline includes 10s fill wait + cancel_algo + 4 DB writes.
		cfg.PerTickTimeout = 45 * time.Second
	}
	return cfg
}

// ExitManagerCollector is the cron-runner adapter for exit evaluation.
type ExitManagerCollector struct {
	em  *execution.ExitManager
	log zerolog.Logger
	cfg ExitManagerConfig
}

// NewExitManagerCollector wires the cron-side adapter.
func NewExitManagerCollector(em *execution.ExitManager, log zerolog.Logger, cfg ExitManagerConfig) *ExitManagerCollector {
	cfg = exitManagerDefaults(cfg)
	return &ExitManagerCollector{em: em, log: log, cfg: cfg}
}

func (c *ExitManagerCollector) Name() string { return "exit_manager" }

func (c *ExitManagerCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.em.EvaluateTick(tickCtx)
	return nil
}
