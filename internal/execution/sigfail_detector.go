// v0.2 Round 3 Module C: signal_fail_detector — 5min cron that checks
// SIGFAIL exit conditions for each open trade and triggers ExitManager.ClosePosition
// when the entry signal has stopped working.
//
// Conditions evaluated (Round 3.x: all 3 are now data-backed):
//   A. OI drop:        current_oi < initial_oi × (1 - OIDropPct)         (oi_history PG)
//   B. EMA20 break:    last N 15m closes all < ema20                     (klines PG + ema20 Redis)
//   C. Price low break: current_price < min(low) × (1 - LowBreakBufferPct) (klines PG window)
//
// Round 3 used Redis "klines:closes:*" for condition B but that key had NO writer
// (sigfail_detector was the only reader). Round 3.x routes B through PG GetLastNCloses
// and adds condition C using klines.low over a time window.
//
// Logic mode (config SIGFAIL_LOGIC):
//   AND — all 3 must trigger (default; conservative for 山寨币 noise)
//   OR  — any 1 triggers (more responsive; higher false-positive risk)
//
// 5min cadence matches the OI + klines refresh; finer would read stale data.
// Round 4 WS will replace with real-time event-driven detection.
//
// ref: docs/V0_2_TRADER_DESIGN.md §4
package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/pkg/indicator"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// Round 3.y constants — EMA20 computed from PG klines, not Redis.
const (
	emaPeriod        = 20 // EMA20 — period in the indicator name
	emaComputeBars   = 30 // period + buffer for stable seed (10 extra bars)
)

// sigfailKlinesTimeframe is the canonical timeframe SIGFAIL pulls from PG.
// The klines collector only writes 15m bars (Round 3.x verify confirmed),
// so 1m/5m semantics from the design doc become "use the 15m proxy".
const sigfailKlinesTimeframe = "15m"

// SigfailDetectorDeps is the minimal DB surface the detector needs.
type SigfailDetectorDeps interface {
	ListOpenTradesForExit(ctx context.Context) ([]gen.ListOpenTradesForExitRow, error)
	GetLatestOI(ctx context.Context, symbol string) (decimal.Decimal, error)
	GetLastNCloses(ctx context.Context, arg gen.GetLastNClosesParams) ([]decimal.Decimal, error)
	GetLowestLowSince(ctx context.Context, arg gen.GetLowestLowSinceParams) (decimal.Decimal, error)
}

// SigfailCloser is the close pipeline (ExitManager.ClosePosition).
type SigfailCloser interface {
	ClosePosition(ctx context.Context, t gen.ListOpenTradesForExitRow, exitReason string, log zerolog.Logger)
}

// SigfailConfig bundles the 5 detection knobs (Round 3.x: 3 conditions).
type SigfailConfig struct {
	OIDropPct   decimal.Decimal // 0.08 = 8% OI drop trigger (condition A)
	EMA20KLines int             // 5 = N consecutive 15m closes below EMA20 (condition B)
	Logic       string          // "AND" or "OR"
	// Condition C — price low break:
	LowBreakBufferPct decimal.Decimal // 0.005 = 0.5% below window-low
	LowLookbackMin    int             // 30 = minutes back (15m TF → 2 bars)
}

// SigfailDetector runs the 5min cron tick.
type SigfailDetector struct {
	db    SigfailDetectorDeps
	closer SigfailCloser
	rdb   *redis.Client
	cfg   SigfailConfig
	log   zerolog.Logger
	nowFn func() time.Time
}

func NewSigfailDetector(db SigfailDetectorDeps, closer SigfailCloser, rdb *redis.Client, cfg SigfailConfig, log zerolog.Logger) *SigfailDetector {
	return &SigfailDetector{db: db, closer: closer, rdb: rdb, cfg: cfg, log: log, nowFn: timez.NowUTC}
}

// DetectTick is the 5min cron entry point. Per-row errors logged; sweep continues.
func (sd *SigfailDetector) DetectTick(ctx context.Context) {
	rows, err := sd.db.ListOpenTradesForExit(ctx)
	if err != nil {
		sd.log.Error().Err(err).Msg("sigfail.tick: list open trades failed")
		metrics.SigfailDetectionRunsTotal.WithLabelValues("err").Inc()
		return
	}
	if len(rows) == 0 {
		metrics.SigfailDetectionRunsTotal.WithLabelValues("empty").Inc()
		return
	}
	triggered := 0
	for _, r := range rows {
		if sd.evalAndMaybeClose(ctx, r) {
			triggered++
		}
	}
	sd.log.Info().Int("open_trades", len(rows)).Int("triggered", triggered).Msg("sigfail.tick")
	metrics.SigfailDetectionRunsTotal.WithLabelValues("ok").Inc()
}

// evalAndMaybeClose returns true if the trade was triggered (and close started).
// Round 3.x: 3 conditions (A: OI drop, B: EMA20 break, C: price low break).
// Data-availability gate: missing data for a condition → that condition not counted.
// AND requires all 3 (A∩B∩C); OR fires on any 1.
func (sd *SigfailDetector) evalAndMaybeClose(ctx context.Context, r gen.ListOpenTradesForExitRow) bool {
	log := sd.log.With().Int64("trade_id", r.ID).Str("symbol", r.Symbol).Logger()

	oiTrigger, oiOK := sd.checkOIDrop(ctx, r, log)
	emaTrigger, emaOK := sd.checkEMA20Break(ctx, r, log)
	lowTrigger, lowOK := sd.checkPriceLowBreak(ctx, r, log)

	var fire bool
	switch sd.cfg.Logic {
	case "OR":
		fire = (oiOK && oiTrigger) || (emaOK && emaTrigger) || (lowOK && lowTrigger)
	default: // AND
		fire = oiOK && emaOK && lowOK && oiTrigger && emaTrigger && lowTrigger
	}

	if !fire {
		log.Debug().
			Bool("oi_ok", oiOK).Bool("oi_trig", oiTrigger).
			Bool("ema_ok", emaOK).Bool("ema_trig", emaTrigger).
			Bool("low_ok", lowOK).Bool("low_trig", lowTrigger).
			Str("logic", sd.cfg.Logic).
			Msg("sigfail.eval: no fire")
		return false
	}

	log.Warn().
		Bool("oi_trig", oiTrigger).
		Bool("ema_trig", emaTrigger).
		Bool("low_trig", lowTrigger).
		Str("logic", sd.cfg.Logic).
		Msg("sigfail.fire: closing trade")
	metrics.SigfailDetectionsTotal.WithLabelValues(r.Symbol, sd.cfg.Logic).Inc()
	sd.closer.ClosePosition(ctx, r, ExitReasonSigfail, log)
	return true
}

// checkOIDrop returns (trigger, ok). ok=false when data unavailable; caller decides
// whether unavailable data counts as "no trigger" or "skip condition".
func (sd *SigfailDetector) checkOIDrop(ctx context.Context, r gen.ListOpenTradesForExitRow, log zerolog.Logger) (bool, bool) {
	initialOI := decimalFromPgNumeric(r.InitialOI)
	if initialOI.IsZero() {
		log.Debug().Msg("sigfail.oi: initial_oi NULL/zero (legacy or entry fetch failed); skip OI condition")
		return false, false
	}
	currentOI, err := sd.db.GetLatestOI(ctx, r.Symbol)
	if err != nil {
		log.Warn().Err(err).Msg("sigfail.oi: GetLatestOI failed; skip OI condition")
		return false, false
	}
	if currentOI.IsZero() {
		log.Warn().Msg("sigfail.oi: current OI zero (oi_history stale?); skip OI condition")
		return false, false
	}
	drop := initialOI.Sub(currentOI).Div(initialOI) // (initial - current) / initial
	trigger := drop.GreaterThanOrEqual(sd.cfg.OIDropPct)
	log.Debug().
		Str("initial_oi", initialOI.String()).
		Str("current_oi", currentOI.String()).
		Str("drop_pct", drop.String()).
		Bool("trigger", trigger).
		Msg("sigfail.oi.eval")
	return trigger, true
}

// checkEMA20Break (Round 3.y): EMA20 computed from PG klines, not Redis.
//
// Redis ema20:{symbol} had transient bugs (observed 2026-05-13 15:55 BJT:
// ESPORTSUSDT ema20=0.00998 when price=0.61 — calculator returned garbage).
// Round 3.y removes the Redis dependency entirely. We pull 30 closes from PG,
// reverse to oldest-first, run indicator.EMA(period=20), and use the result
// in-process. Eliminates a whole class of stale/garbage failures.
//
// Returns (trigger, ok). ok=false when closes insufficient or EMA compute fails.
func (sd *SigfailDetector) checkEMA20Break(ctx context.Context, r gen.ListOpenTradesForExitRow, log zerolog.Logger) (bool, bool) {
	closes, err := sd.db.GetLastNCloses(ctx, gen.GetLastNClosesParams{
		Symbol:    r.Symbol,
		Timeframe: sigfailKlinesTimeframe,
		Limit:     emaComputeBars,
	})
	if err != nil || len(closes) < emaPeriod {
		log.Debug().Err(err).Int("got", len(closes)).Int("need", emaPeriod).
			Msg("sigfail.ema20: closes insufficient for EMA seed; skip")
		return false, false
	}
	if len(closes) < sd.cfg.EMA20KLines {
		log.Debug().Int("got", len(closes)).Int("need_check", sd.cfg.EMA20KLines).
			Msg("sigfail.ema20: closes shorter than check window; skip")
		return false, false
	}
	// PG returns newest-first; indicator.EMA expects oldest-first.
	ordered := make([]decimal.Decimal, len(closes))
	for i, c := range closes {
		ordered[len(closes)-1-i] = c
	}
	ema, err := indicator.EMA(ordered, emaPeriod)
	if err != nil || ema.IsZero() {
		log.Warn().Err(err).Msg("sigfail.ema20: indicator.EMA compute failed; skip")
		return false, false
	}
	// Check the most recent EMA20KLines closes (still newest-first in `closes`) all < EMA.
	for i := 0; i < sd.cfg.EMA20KLines; i++ {
		if !closes[i].LessThan(ema) {
			log.Debug().Str("close", closes[i].String()).Str("ema", ema.String()).
				Msg("sigfail.ema20.eval: at least one close ≥ EMA → no trigger")
			return false, true
		}
	}
	log.Debug().Str("ema", ema.String()).Int("n", sd.cfg.EMA20KLines).
		Msg("sigfail.ema20.eval: all closes < EMA → trigger")
	return true, true
}

// checkPriceLowBreak (Round 3.x condition C): current_price < window_low × (1 - buffer).
// Window = last LowLookbackMin minutes of 15m klines (MIN(low) across the bars).
// current_price source: Redis latest_price:{symbol} (1min update, written by position_price collector).
// Returns (trigger, ok). ok=false when current price or window low unavailable.
func (sd *SigfailDetector) checkPriceLowBreak(ctx context.Context, r gen.ListOpenTradesForExitRow, log zerolog.Logger) (bool, bool) {
	if sd.cfg.LowLookbackMin <= 0 || sd.cfg.LowBreakBufferPct.IsZero() {
		log.Debug().Msg("sigfail.low: lookback/buffer zero; skip condition C")
		return false, false
	}
	current, err := sd.getCurrentPrice(ctx, r.Symbol)
	if err != nil || current.IsZero() {
		log.Debug().Err(err).Msg("sigfail.low: latest_price unavailable; skip condition C")
		return false, false
	}
	cutoff := sd.nowFn().Add(-time.Duration(sd.cfg.LowLookbackMin) * time.Minute)
	low, err := sd.db.GetLowestLowSince(ctx, gen.GetLowestLowSinceParams{
		Symbol: r.Symbol, Timeframe: sigfailKlinesTimeframe, OpenTime: cutoff,
	})
	if err != nil || low.IsZero() {
		log.Debug().Err(err).Msg("sigfail.low: window low unavailable (empty window?); skip condition C")
		return false, false
	}
	threshold := low.Mul(decimal.NewFromInt(1).Sub(sd.cfg.LowBreakBufferPct))
	trigger := current.LessThan(threshold)
	log.Debug().
		Str("current", current.String()).
		Str("window_low", low.String()).
		Str("threshold", threshold.String()).
		Int("lookback_min", sd.cfg.LowLookbackMin).
		Bool("trigger", trigger).
		Msg("sigfail.low.eval")
	return trigger, true
}

// getCurrentPrice mirrors trail_upgrader.getCurrentPrice (Redis latest_price:{symbol}).
func (sd *SigfailDetector) getCurrentPrice(ctx context.Context, symbol string) (decimal.Decimal, error) {
	raw, err := sd.rdb.Get(ctx, "latest_price:"+symbol).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return decimal.Zero, fmt.Errorf("latest_price not in redis")
		}
		return decimal.Zero, err
	}
	p, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse latest_price %q: %w", raw, err)
	}
	return p, nil
}


