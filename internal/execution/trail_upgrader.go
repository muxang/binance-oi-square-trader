// v0.2 Round 1 Module B: 4-stage trailing stop upgrader.
//
// Each 5min cron tick this iterates open trades and:
//   1. Updates trail_high_price = max(current_price, db.trail_high_price)
//   2. Dispatches on trail_stage:
//      S0 → activate S1 when pct_gain ≥ Stage1ActivatePct (0.03)
//      S1 → upgrade to S2 when pct_gain ≥ Stage2UpgradePct (0.15)
//      S2 → upgrade to S3 (trader-managed) when pct_gain ≥ Stage3UpgradePct (0.30)
//      S3 → upgrade to S4 when pct_gain ≥ Stage4UpgradePct (0.60); else ratchet stop
//      S4 → ratchet stop when trail_high moves up
//
// S1/S2 use Binance native TRAILING_STOP_MARKET (callbackRate ≤ 5%). S3/S4 use
// trader-managed STOP_MARKET because callbackRate 10%/15% exceed Binance's 5%
// upper bound; we re-place the stop higher as trail_high ratchets up.
//
// FINISHED detection (algo triggered + filled) stays on algo_reconciler — the
// disaster_stop and trail Algos are independent algoIds. algo_reconciler.go
// owns disaster_stop_order_id; this file owns binance_trail_algo_id.
//
// ref: docs/V0_2_TRADER_DESIGN.md §3
package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// TrailUpgraderDeps is the minimal DB surface this collector needs.
type TrailUpgraderDeps interface {
	ListOpenTradesForTrail(ctx context.Context) ([]gen.ListOpenTradesForTrailRow, error)
	UpdateTradeTrailActivate(ctx context.Context, arg gen.UpdateTradeTrailActivateParams) error
	UpdateTradeTrailStage(ctx context.Context, arg gen.UpdateTradeTrailStageParams) error
	UpdateTradeTrailHigh(ctx context.Context, arg gen.UpdateTradeTrailHighParams) error
}

// TrailBinanceClient is the minimal binance surface (production wires *binance.Client).
type TrailBinanceClient interface {
	PlaceAlgoTrailingStop(ctx context.Context, symbol, qty, activationPrice string, callbackRate float64) (binance.AlgoOrderResult, error)
	PlaceAlgoConditionalStop(ctx context.Context, symbol, qty, triggerPrice string) (binance.AlgoOrderResult, error)
	CancelAlgoOrder(ctx context.Context, symbol string, algoID int64) error
}

// TickSizeFetcher resolves tickSize per symbol for price rounding (Binance -1111).
type TickSizeFetcher interface {
	GetTradingFilters(ctx context.Context, symbol string) (binance.TradingFilters, error)
}

// TrailConfig bundles the 8 thresholds (all decimal — Binance % is conv at use site).
type TrailConfig struct {
	Stage1ActivatePct  decimal.Decimal // 0.03
	Stage1CallbackRate decimal.Decimal // 0.03 (× 100 → Binance 3%)
	Stage2UpgradePct   decimal.Decimal // 0.15
	Stage2CallbackRate decimal.Decimal // 0.05 (× 100 → Binance 5%, the upper bound)
	Stage3UpgradePct   decimal.Decimal // 0.30
	Stage3CallbackRate decimal.Decimal // 0.10 (trader-managed, applied to trail_high)
	Stage4UpgradePct   decimal.Decimal // 0.60
	Stage4CallbackRate decimal.Decimal // 0.15
	// v0.2 Round 1.y: ratchet deadband. S3/S4 re-place only if trail_high advanced
	// by >= RatchetMinPct since last persisted high. Zero disables (re-arm every tick).
	RatchetMinPct decimal.Decimal // e.g. 0.005 = 0.5%
}

// TrailUpgrader runs the 5min trail tick.
type TrailUpgrader struct {
	db    TrailUpgraderDeps
	bc    TrailBinanceClient
	tf    TickSizeFetcher
	rdb   *redis.Client
	cfg   TrailConfig
	log   zerolog.Logger
	nowFn func() time.Time
}

func NewTrailUpgrader(db TrailUpgraderDeps, bc TrailBinanceClient, tf TickSizeFetcher, rdb *redis.Client, cfg TrailConfig, log zerolog.Logger) *TrailUpgrader {
	return &TrailUpgrader{db: db, bc: bc, tf: tf, rdb: rdb, cfg: cfg, log: log, nowFn: timez.NowUTC}
}

// ReconcileTick is the 5min cron entry point. Per-row errors are logged and
// don't abort the sweep (best-effort, retries next tick).
func (tu *TrailUpgrader) ReconcileTick(ctx context.Context) {
	rows, err := tu.db.ListOpenTradesForTrail(ctx)
	if err != nil {
		tu.log.Error().Err(err).Msg("trail.tick: list failed")
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, r := range rows {
		tu.handleRow(ctx, r)
	}
	tu.log.Info().Int("rows", len(rows)).Msg("trail.tick: complete")
}

func (tu *TrailUpgrader) handleRow(ctx context.Context, r gen.ListOpenTradesForTrailRow) {
	log := tu.log.With().Int64("trade_id", r.ID).Str("symbol", r.Symbol).Int16("trail_stage", r.TrailStage).Logger()

	current, err := tu.getCurrentPrice(ctx, r.Symbol)
	if err != nil {
		log.Warn().Err(err).Msg("trail: latest_price unavailable, skip tick")
		return
	}
	entry := decimalFromPgNumeric(r.EntryPrice)
	if entry.IsZero() {
		log.Error().Msg("trail: entry_price zero, skip")
		return
	}
	qty := decimalFromPgNumeric(r.CurrentQty)
	if qty.IsZero() {
		log.Warn().Msg("trail: current_qty zero, skip (TP may have fully closed; disaster stop covers)")
		return
	}
	prevHigh := decimalFromPgNumeric(r.TrailHighPrice)
	newHigh := prevHigh
	if current.GreaterThan(newHigh) {
		newHigh = current
	}
	pctGain := current.Sub(entry).Div(entry)

	switch r.TrailStage {
	case 0:
		if pctGain.GreaterThanOrEqual(tu.cfg.Stage1ActivatePct) {
			tu.activateS1(ctx, r, current, qty, log)
		}
	case 1:
		if pctGain.GreaterThanOrEqual(tu.cfg.Stage2UpgradePct) {
			tu.upgradeBinanceNative(ctx, r, 2, tu.cfg.Stage2CallbackRate, current, qty, log)
		} else {
			tu.persistTrailHigh(ctx, r.ID, prevHigh, newHigh, log)
		}
	case 2:
		if pctGain.GreaterThanOrEqual(tu.cfg.Stage3UpgradePct) {
			tu.upgradeToTraderManaged(ctx, r, 3, tu.cfg.Stage3CallbackRate, newHigh, qty, log)
		} else {
			tu.persistTrailHigh(ctx, r.ID, prevHigh, newHigh, log)
		}
	case 3:
		if pctGain.GreaterThanOrEqual(tu.cfg.Stage4UpgradePct) {
			tu.upgradeTraderManagedStage(ctx, r, 4, tu.cfg.Stage4CallbackRate, newHigh, qty, log)
		} else if tu.shouldRatchet(prevHigh, newHigh) {
			tu.rearmTraderManaged(ctx, r, 3, tu.cfg.Stage3CallbackRate, newHigh, qty, log)
		} else if newHigh.GreaterThan(prevHigh) {
			// High advanced but below deadband — persist high only (no algo churn).
			tu.persistTrailHigh(ctx, r.ID, prevHigh, newHigh, log)
		}
	case 4:
		if tu.shouldRatchet(prevHigh, newHigh) {
			tu.rearmTraderManaged(ctx, r, 4, tu.cfg.Stage4CallbackRate, newHigh, qty, log)
		} else if newHigh.GreaterThan(prevHigh) {
			tu.persistTrailHigh(ctx, r.ID, prevHigh, newHigh, log)
		}
	}
}

// shouldRatchet returns true when the new high moved up by ≥ RatchetMinPct (deadband).
// Zero RatchetMinPct → re-arm on any upward move (legacy behavior).
func (tu *TrailUpgrader) shouldRatchet(prev, next decimal.Decimal) bool {
	if !next.GreaterThan(prev) {
		return false
	}
	if tu.cfg.RatchetMinPct.IsZero() || prev.IsZero() {
		return true
	}
	delta := next.Sub(prev).Div(prev)
	return delta.GreaterThanOrEqual(tu.cfg.RatchetMinPct)
}

// getCurrentPrice reads latest_price:{symbol} (string decimal, set by position_price collector).
func (tu *TrailUpgrader) getCurrentPrice(ctx context.Context, symbol string) (decimal.Decimal, error) {
	raw, err := tu.rdb.Get(ctx, "latest_price:"+symbol).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return decimal.Zero, fmt.Errorf("latest_price not in redis (price collector stale?)")
		}
		return decimal.Zero, err
	}
	p, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse latest_price %q: %w", raw, err)
	}
	if p.IsZero() || p.IsNegative() {
		return decimal.Zero, fmt.Errorf("invalid latest_price %s", raw)
	}
	return p, nil
}

// persistTrailHigh writes the new high if it moved up (non-fatal on error).
func (tu *TrailUpgrader) persistTrailHigh(ctx context.Context, tradeID int64, prev, next decimal.Decimal, log zerolog.Logger) {
	if !next.GreaterThan(prev) {
		return
	}
	if err := tu.db.UpdateTradeTrailHigh(ctx, gen.UpdateTradeTrailHighParams{ID: tradeID, TrailHighPrice: next}); err != nil {
		log.Warn().Err(err).Msg("trail: UpdateTrailHigh failed (non-fatal)")
	}
}

// activateS1 places the first Binance native TRAILING_STOP_MARKET (callbackRate 3%).
func (tu *TrailUpgrader) activateS1(ctx context.Context, r gen.ListOpenTradesForTrailRow, current, qty decimal.Decimal, log zerolog.Logger) {
	activation := tu.roundPrice(ctx, r.Symbol, current, log)
	cb := mustFloat(tu.cfg.Stage1CallbackRate.Mul(decimal.NewFromInt(100)))
	res, err := tu.bc.PlaceAlgoTrailingStop(ctx, r.Symbol, qty.String(), activation.String(), cb)
	if err != nil {
		log.Error().Err(err).Msg("trail.activate.S1: place failed")
		return
	}
	algoID := strconv.FormatInt(res.AlgoID, 10)
	if err := tu.db.UpdateTradeTrailActivate(ctx, gen.UpdateTradeTrailActivateParams{
		ID:                 r.ID,
		BinanceTrailAlgoID: pgtype.Text{String: algoID, Valid: true},
		Price:              activation,
	}); err != nil {
		log.Error().Err(err).Msg("trail.activate.S1: DB update failed (algo placed but trade not flagged)")
		return
	}
	metrics.TrailingStageUpgradeTotal.WithLabelValues("0", "1").Inc()
	log.Info().Str("algo_id", algoID).Str("activation", activation.String()).Float64("callback_rate_pct", cb).Msg("trail.activate.S1")
}

// upgradeBinanceNative handles S1→S2: cancel old TRAILING_STOP_MARKET, place new with callback 5%.
func (tu *TrailUpgrader) upgradeBinanceNative(ctx context.Context, r gen.ListOpenTradesForTrailRow, toStage int16, cbRate, current, qty decimal.Decimal, log zerolog.Logger) {
	if !r.BinanceTrailAlgoID.Valid {
		log.Warn().Msg("trail.upgrade.binance: no algo_id, skipping (state corrupt — disaster stop covers)")
		return
	}
	oldID, err := strconv.ParseInt(r.BinanceTrailAlgoID.String, 10, 64)
	if err != nil {
		log.Warn().Err(err).Msg("trail.upgrade.binance: invalid old algo_id")
		return
	}
	if err := tu.bc.CancelAlgoOrder(ctx, r.Symbol, oldID); err != nil {
		log.Error().Err(err).Int64("old_algo_id", oldID).Msg("trail.upgrade.binance: cancel failed, abort upgrade (old algo retained)")
		return
	}
	activation := tu.roundPrice(ctx, r.Symbol, current, log)
	cb := mustFloat(cbRate.Mul(decimal.NewFromInt(100)))
	res, err := tu.bc.PlaceAlgoTrailingStop(ctx, r.Symbol, qty.String(), activation.String(), cb)
	if err != nil {
		log.Error().Err(err).Msg("trail.upgrade.binance: place new failed (CRITICAL — old cancelled, no trail; disaster stop covers)")
		// Clear stale algo_id so next tick can attempt to re-activate fresh
		_ = tu.db.UpdateTradeTrailStage(ctx, gen.UpdateTradeTrailStageParams{ID: r.ID, TrailStage: 0, BinanceTrailAlgoID: pgtype.Text{}})
		return
	}
	newID := strconv.FormatInt(res.AlgoID, 10)
	if err := tu.db.UpdateTradeTrailStage(ctx, gen.UpdateTradeTrailStageParams{
		ID:                 r.ID,
		TrailStage:         toStage,
		BinanceTrailAlgoID: pgtype.Text{String: newID, Valid: true},
	}); err != nil {
		log.Error().Err(err).Msg("trail.upgrade.binance: DB update failed")
		return
	}
	metrics.TrailingStageUpgradeTotal.WithLabelValues(strconv.Itoa(int(r.TrailStage)), strconv.Itoa(int(toStage))).Inc()
	log.Info().Str("new_algo_id", newID).Int16("to_stage", toStage).Float64("callback_rate_pct", cb).Msg("trail.upgrade.binance_native")
}

// upgradeToTraderManaged S2→S3: cancel Binance trailing, place STOP_MARKET at high × (1-callback).
func (tu *TrailUpgrader) upgradeToTraderManaged(ctx context.Context, r gen.ListOpenTradesForTrailRow, toStage int16, cbRate, trailHigh, qty decimal.Decimal, log zerolog.Logger) {
	if r.BinanceTrailAlgoID.Valid {
		oldID, err := strconv.ParseInt(r.BinanceTrailAlgoID.String, 10, 64)
		if err == nil {
			if cancelErr := tu.bc.CancelAlgoOrder(ctx, r.Symbol, oldID); cancelErr != nil {
				log.Error().Err(cancelErr).Int64("old_algo_id", oldID).Msg("trail.upgrade.to_managed: cancel S2 failed, abort upgrade")
				return
			}
		}
	}
	tu.placeTraderManagedStop(ctx, r, toStage, cbRate, trailHigh, qty, "trail.upgrade.to_trader_managed", log)
}

// upgradeTraderManagedStage S3→S4: same trader-managed model, just different callback.
func (tu *TrailUpgrader) upgradeTraderManagedStage(ctx context.Context, r gen.ListOpenTradesForTrailRow, toStage int16, cbRate, trailHigh, qty decimal.Decimal, log zerolog.Logger) {
	if r.BinanceTrailAlgoID.Valid {
		oldID, err := strconv.ParseInt(r.BinanceTrailAlgoID.String, 10, 64)
		if err == nil {
			if cancelErr := tu.bc.CancelAlgoOrder(ctx, r.Symbol, oldID); cancelErr != nil {
				log.Error().Err(cancelErr).Int64("old_algo_id", oldID).Msg("trail.upgrade.S3_S4: cancel failed, abort upgrade")
				return
			}
		}
	}
	tu.placeTraderManagedStop(ctx, r, toStage, cbRate, trailHigh, qty, "trail.upgrade.S3_S4", log)
}

// rearmTraderManaged ratchets the trader-managed STOP_MARKET up when trail_high advances.
func (tu *TrailUpgrader) rearmTraderManaged(ctx context.Context, r gen.ListOpenTradesForTrailRow, stage int16, cbRate, trailHigh, qty decimal.Decimal, log zerolog.Logger) {
	if r.BinanceTrailAlgoID.Valid {
		oldID, err := strconv.ParseInt(r.BinanceTrailAlgoID.String, 10, 64)
		if err == nil {
			if cancelErr := tu.bc.CancelAlgoOrder(ctx, r.Symbol, oldID); cancelErr != nil {
				log.Warn().Err(cancelErr).Int64("old_algo_id", oldID).Msg("trail.rearm: cancel failed (continuing, may create duplicate; next tick reconciles)")
			}
		}
	}
	tu.placeTraderManagedStop(ctx, r, stage, cbRate, trailHigh, qty, "trail.rearm", log)
}

// placeTraderManagedStop computes stop = high × (1 - callback), rounds to tick,
// places STOP_MARKET via Algo Service, and persists stage + new algo id.
func (tu *TrailUpgrader) placeTraderManagedStop(ctx context.Context, r gen.ListOpenTradesForTrailRow, stage int16, cbRate, trailHigh, qty decimal.Decimal, evt string, log zerolog.Logger) {
	stopRaw := trailHigh.Mul(decimal.NewFromInt(1).Sub(cbRate))
	stop := tu.roundPrice(ctx, r.Symbol, stopRaw, log)
	res, err := tu.bc.PlaceAlgoConditionalStop(ctx, r.Symbol, qty.String(), stop.String())
	if err != nil {
		log.Error().Err(err).Str("stop_price", stop.String()).Msg(evt + ": place STOP_MARKET failed (disaster stop covers)")
		return
	}
	newID := strconv.FormatInt(res.AlgoID, 10)
	if err := tu.db.UpdateTradeTrailStage(ctx, gen.UpdateTradeTrailStageParams{
		ID:                 r.ID,
		TrailStage:         stage,
		BinanceTrailAlgoID: pgtype.Text{String: newID, Valid: true},
	}); err != nil {
		log.Error().Err(err).Msg(evt + ": DB update failed (algo placed)")
		return
	}
	// Persist high too — the stop's anchor.
	if err := tu.db.UpdateTradeTrailHigh(ctx, gen.UpdateTradeTrailHighParams{ID: r.ID, TrailHighPrice: trailHigh}); err != nil {
		log.Warn().Err(err).Msg(evt + ": UpdateTrailHigh failed (non-fatal)")
	}
	if r.TrailStage != stage {
		metrics.TrailingStageUpgradeTotal.WithLabelValues(strconv.Itoa(int(r.TrailStage)), strconv.Itoa(int(stage))).Inc()
	}
	log.Info().
		Str("new_algo_id", newID).
		Str("trail_high", trailHigh.String()).
		Str("stop_price", stop.String()).
		Str("callback_rate", cbRate.String()).
		Int16("stage", stage).
		Msg(evt)
}

// roundPrice rounds to the symbol's tickSize via SymbolService (truncate, conservative re slippage).
// On lookup failure: returns price unrounded + warns (Binance may -1111 but the
// disaster stop covers; we don't want to skip the trail action over this).
func (tu *TrailUpgrader) roundPrice(ctx context.Context, symbol string, price decimal.Decimal, log zerolog.Logger) decimal.Decimal {
	f, err := tu.tf.GetTradingFilters(ctx, symbol)
	if err != nil || f.TickSize.IsZero() {
		log.Warn().Err(err).Str("symbol", symbol).Msg("trail: tickSize unavailable, using unrounded price")
		return price
	}
	return price.Div(f.TickSize).Truncate(0).Mul(f.TickSize)
}
