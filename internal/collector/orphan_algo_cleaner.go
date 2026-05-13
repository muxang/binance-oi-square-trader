// Round R.3: orphan_algo_cleaner cron wrapper — 1min sweep that cancels
// SELL reduceOnly Algos whose Binance position is already closed.

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type OrphanAlgoCleanerConfig struct {
	PerTickTimeout time.Duration
}

func orphanAlgoCleanerDefaults(cfg OrphanAlgoCleanerConfig) OrphanAlgoCleanerConfig {
	if cfg.PerTickTimeout == 0 {
		// One ListOpenAlgoOrders + one GetPositionRisk + N×CancelAlgoOrder.
		// 60s budget covers worst case ~10 orphan cancels at slow proxy.
		cfg.PerTickTimeout = 60 * time.Second
	}
	return cfg
}

type OrphanAlgoCleanerCollector struct {
	oc  *execution.OrphanAlgoCleaner
	log zerolog.Logger
	cfg OrphanAlgoCleanerConfig
}

func NewOrphanAlgoCleanerCollector(oc *execution.OrphanAlgoCleaner, log zerolog.Logger, cfg OrphanAlgoCleanerConfig) *OrphanAlgoCleanerCollector {
	cfg = orphanAlgoCleanerDefaults(cfg)
	return &OrphanAlgoCleanerCollector{oc: oc, log: log, cfg: cfg}
}

func (c *OrphanAlgoCleanerCollector) Name() string { return "orphan_algo_cleaner" }

func (c *OrphanAlgoCleanerCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.oc.ReconcileTick(tickCtx)
	return nil
}
