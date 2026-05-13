// v0.2 Round 3 Module C: signal_fail_detector — 5min cron that checks
// SIGFAIL exit conditions for each open trade and triggers ExitManager.ClosePosition
// when the entry signal has stopped working.
//
// Conditions evaluated:
//   A. OI drop:  current_oi < initial_oi × (1 - OIDropPct)  (e.g. 8%)
//   B. EMA20 break:  last EMA20KLines closes all < ema20 (15m timeframe)
//   C. (deferred to forward calibration — see docs/V0_2_TRADER_DESIGN.md §4.4)
//
// Logic mode (config SIGFAIL_LOGIC):
//   AND — both A and B must trigger (default; conservative for 山寨币 noise)
//   OR  — either A or B triggers (more responsive; higher false-positive risk)
//
// 5min cadence chosen to match the OI collector (5min) and klines/EMA refresh
// (5min) — finer-grained polls would just read stale Redis values. Round 4 WS
// will replace this with real-time event-driven detection.
//
// ref: docs/V0_2_TRADER_DESIGN.md §4
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// SigfailDetectorDeps is the minimal DB surface the detector needs.
type SigfailDetectorDeps interface {
	ListOpenTradesForExit(ctx context.Context) ([]gen.ListOpenTradesForExitRow, error)
	GetLatestOI(ctx context.Context, symbol string) (decimal.Decimal, error)
}

// SigfailCloser is the close pipeline (ExitManager.ClosePosition).
type SigfailCloser interface {
	ClosePosition(ctx context.Context, t gen.ListOpenTradesForExitRow, exitReason string, log zerolog.Logger)
}

// SigfailConfig bundles the 3 detection knobs.
type SigfailConfig struct {
	OIDropPct   decimal.Decimal // 0.08 = 8% OI drop trigger
	EMA20KLines int             // 5 = N consecutive 15m closes below EMA20
	Logic       string          // "AND" or "OR"
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
func (sd *SigfailDetector) evalAndMaybeClose(ctx context.Context, r gen.ListOpenTradesForExitRow) bool {
	log := sd.log.With().Int64("trade_id", r.ID).Str("symbol", r.Symbol).Logger()

	oiTrigger, oiOK := sd.checkOIDrop(ctx, r, log)
	emaTrigger, emaOK := sd.checkEMA20Break(ctx, r, log)

	// At least one condition source must be readable (data quality gate).
	// Logic AND with one missing condition → don't fire (safer); OR fires if the readable one triggered.
	var fire bool
	switch sd.cfg.Logic {
	case "OR":
		if (oiOK && oiTrigger) || (emaOK && emaTrigger) {
			fire = true
		}
	default: // "AND" (default + safer)
		if oiOK && emaOK && oiTrigger && emaTrigger {
			fire = true
		}
	}

	if !fire {
		log.Debug().
			Bool("oi_ok", oiOK).Bool("oi_trig", oiTrigger).
			Bool("ema_ok", emaOK).Bool("ema_trig", emaTrigger).
			Str("logic", sd.cfg.Logic).
			Msg("sigfail.eval: no fire")
		return false
	}

	log.Warn().
		Bool("oi_trig", oiTrigger).Bool("ema_trig", emaTrigger).
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

// checkEMA20Break: last N 15m closes (Redis klines:closes:{symbol}:15m) all < EMA20.
// Returns (trigger, ok). ok=false on data unavailable.
//
// Data model: position_price collector + klines collector keep
//   ema20:{symbol}      → indicatorPayload JSON ({"value":"X","computed_at":"..."})
//   klines:closes:{symbol}:15m → Redis list of last N closes (newest first)
// If the closes list isn't populated yet (v0.2 Round 3 install), this returns ok=false.
func (sd *SigfailDetector) checkEMA20Break(ctx context.Context, r gen.ListOpenTradesForExitRow, log zerolog.Logger) (bool, bool) {
	ema, err := sd.getEMA20(ctx, r.Symbol)
	if err != nil || ema.IsZero() {
		log.Debug().Err(err).Msg("sigfail.ema20: EMA unavailable; skip EMA condition")
		return false, false
	}
	closes, err := sd.getLastNCloses(ctx, r.Symbol, sd.cfg.EMA20KLines)
	if err != nil || len(closes) < sd.cfg.EMA20KLines {
		log.Debug().Err(err).Int("got", len(closes)).Msg("sigfail.ema20: closes list short; skip EMA condition")
		return false, false
	}
	for _, c := range closes {
		if !c.LessThan(ema) {
			log.Debug().Str("close", c.String()).Str("ema", ema.String()).Msg("sigfail.ema20.eval: at least one close ≥ EMA → no trigger")
			return false, true
		}
	}
	log.Debug().Str("ema", ema.String()).Int("n", sd.cfg.EMA20KLines).Msg("sigfail.ema20.eval: all closes < EMA → trigger")
	return true, true
}

// getEMA20 reads ema20:{symbol} (JSON payload from klines collector).
func (sd *SigfailDetector) getEMA20(ctx context.Context, symbol string) (decimal.Decimal, error) {
	raw, err := sd.rdb.Get(ctx, "ema20:"+symbol).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return decimal.Zero, fmt.Errorf("ema20 not in redis")
		}
		return decimal.Zero, err
	}
	var p struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Value == "" {
		return decimal.Zero, fmt.Errorf("parse ema20 payload: %w", err)
	}
	v, err := decimal.NewFromString(p.Value)
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse ema20 decimal: %w", err)
	}
	return v, nil
}

// getLastNCloses reads klines:closes:{symbol}:15m Redis list (newest first).
// Returns up to n closes. Empty list → caller skips EMA condition.
func (sd *SigfailDetector) getLastNCloses(ctx context.Context, symbol string, n int) ([]decimal.Decimal, error) {
	if n <= 0 {
		return nil, fmt.Errorf("getLastNCloses: n must be > 0")
	}
	raws, err := sd.rdb.LRange(ctx, "klines:closes:"+symbol+":15m", 0, int64(n-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]decimal.Decimal, 0, len(raws))
	for _, s := range raws {
		d, err := decimal.NewFromString(s)
		if err != nil {
			continue // skip malformed entries; len check at caller handles
		}
		out = append(out, d)
	}
	return out, nil
}
