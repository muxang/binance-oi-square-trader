// v0.2 Gap 1: algo_polling cron — 1min poll of open trades' disaster-stop
// Algo orders to detect FINISHED status and auto-close. Wraps
// execution.AlgoReconciler.ReconcileTick in the standard collector contract.

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type AlgoPollingConfig struct {
	PerTickTimeout time.Duration
}

func algoPollingDefaults(cfg AlgoPollingConfig) AlgoPollingConfig {
	if cfg.PerTickTimeout == 0 {
		// QueryAlgoOrder weight=1 per call; ~20 open trades worst case →
		// 30s budget leaves margin for slow proxy.
		cfg.PerTickTimeout = 30 * time.Second
	}
	return cfg
}

// AlgoPollingCollector wraps AlgoReconciler for the cron runner.
type AlgoPollingCollector struct {
	ar  *execution.AlgoReconciler
	log zerolog.Logger
	cfg AlgoPollingConfig
}

func NewAlgoPollingCollector(ar *execution.AlgoReconciler, log zerolog.Logger, cfg AlgoPollingConfig) *AlgoPollingCollector {
	cfg = algoPollingDefaults(cfg)
	return &AlgoPollingCollector{ar: ar, log: log, cfg: cfg}
}

func (c *AlgoPollingCollector) Name() string { return "algo_polling" }

func (c *AlgoPollingCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.ar.ReconcileTick(tickCtx)
	return nil
}
