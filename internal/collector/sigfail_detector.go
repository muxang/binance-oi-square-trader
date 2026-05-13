// v0.2 Round 3 Module C: signal_fail_detector cron — 5min sweep that evaluates
// SIGFAIL conditions (OI drop + EMA20 break) per open trade.

package collector

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/execution"
)

type SigfailDetectorConfig struct {
	PerTickTimeout time.Duration
}

func sigfailDetectorDefaults(cfg SigfailDetectorConfig) SigfailDetectorConfig {
	if cfg.PerTickTimeout == 0 {
		// Each open trade: 1× GetLatestOI (PG) + 1× Redis GET ema20 + 1× LRANGE klines.
		// 20 trades × 3 calls ≈ 60 ops; 90s budget covers slow proxy + PG.
		cfg.PerTickTimeout = 90 * time.Second
	}
	return cfg
}

type SigfailDetectorCollector struct {
	sd  *execution.SigfailDetector
	log zerolog.Logger
	cfg SigfailDetectorConfig
}

func NewSigfailDetectorCollector(sd *execution.SigfailDetector, log zerolog.Logger, cfg SigfailDetectorConfig) *SigfailDetectorCollector {
	cfg = sigfailDetectorDefaults(cfg)
	return &SigfailDetectorCollector{sd: sd, log: log, cfg: cfg}
}

func (c *SigfailDetectorCollector) Name() string { return "sigfail_detector" }

func (c *SigfailDetectorCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()
	c.sd.DetectTick(tickCtx)
	return nil
}
