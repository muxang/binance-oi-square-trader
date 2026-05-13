// Phase 4 Round 3: position manager — 1min cron sync of open positions
// against /fapi/v3/positionRisk + Redis zset rebuild + MARGIN_CALL detect.
//
// Out of scope (Round 4+): bidirectional reconciliation halt + RCA prompts;
// Round 3 logs drift but does NOT trip circuit_breaker on its own.
//
// MARGIN_CALL: per SPEC §8, margin_ratio = (-unrealized_pnl) / margin > 0.8
// triggers emergency MARKET SELL reduceOnly. Phase 4 Round 1 灾难止损 @ -6%
// fires earlier via Algo Service; MARGIN_CALL is the secondary safety net
// when Algo somehow doesn't trip (e.g., 1min ticker is the slow fallback for
// the WS User Data Stream that Phase 4 Round 7+ adds).

package execution

import (
	"context"
	"encoding/json"
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

const (
	redisKeyPositionsActive = "positions_active" // zset trade_id by entered_at
	marginCallRatioTrigger  = 0.8                // SPEC §8
	// Round 4 reconcile thresholds + halt window
	driftHaltThresholdPct = 0.05         // > 5% qty drift trips halt (Round 3 logged at 1%)
	reconcileHaltDuration = 1 * time.Hour // halt_until = NOW + 1h for reconcile halts
)

// Round 4 halt_type label values written to halt_rca.halt_type.
const (
	haltTypeLocalOrphan = "local_only_orphan"
	haltTypeBinanceOnly = "binance_only_unknown"
	haltTypeDriftExceed = "drift_exceeded"
)

// PositionSyncDeps is the minimal DB surface position_manager needs.
type PositionSyncDeps interface {
	ListOpenTradesForSync(ctx context.Context) ([]gen.ListOpenTradesForSyncRow, error)
	UpdatePositionStateSync(ctx context.Context, arg gen.UpdatePositionStateSyncParams) error
	UpdateTradeFailed(ctx context.Context, arg gen.UpdateTradeFailedParams) error
	InsertTradeExit(ctx context.Context, arg gen.InsertTradeExitParams) error
	// v0.2 Catch 5: emergencyExit needs to clean up position_states like the
	// regular close pipeline does (exit_manager.persistClose).
	DeletePositionState(ctx context.Context, tradeID int64) error
	// Round 4: write halt RCA + trip generic halt.
	InsertHaltRCA(ctx context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error)
	TripGenericHalt(ctx context.Context, arg gen.TripGenericHaltParams) error
}

// PositionRiskFetcher is the minimal binance surface.
type PositionRiskFetcher interface {
	GetPositionRisk(ctx context.Context, symbol string) ([]binance.PositionRisk, error)
	PlaceMarketOrder(ctx context.Context, symbol, side, quantity, clientOrderID string) (binance.OrderResult, error)
}

// PositionManager owns the 1min position sync + MARGIN_CALL emergency exit.
// Public methods are safe for concurrent use (one goroutine per cron tick).
type PositionManager struct {
	db    PositionSyncDeps
	bc    PositionRiskFetcher
	rdb   *redis.Client
	log   zerolog.Logger
	nowFn func() time.Time
	// v0.2 Step 5: optional algo reconciler for race-window defense. When set,
	// the local_only_orphan branch consults it before tripping halt — if Algo
	// is FINISHED, auto-close instead. nil = legacy behavior (always halt).
	algoReconciler *AlgoReconciler
}

// NewPositionManager constructs a PositionManager.
func NewPositionManager(db PositionSyncDeps, bc PositionRiskFetcher, rdb *redis.Client, log zerolog.Logger) *PositionManager {
	return &PositionManager{db: db, bc: bc, rdb: rdb, log: log, nowFn: timez.NowUTC}
}

// SetAlgoReconciler wires the v0.2 Step 5 race-window defense. Called from
// main.go after both PositionManager and AlgoReconciler are constructed
// (mutual dependency would otherwise require a constructor refactor).
func (pm *PositionManager) SetAlgoReconciler(ar *AlgoReconciler) {
	pm.algoReconciler = ar
}

// SyncTick runs one sync pass. Called from cron every 1min.
// Steps:
//  1. List open trades from DB.
//  2. Pull all positions from /fapi/v3/positionRisk.
//  3. For each open trade: find matching binance position, detect drift,
//     update position_states (current_qty + highest_price + last_check_ts),
//     compute margin_ratio, trigger emergency exit if > 0.8.
//  4. Rebuild Redis zset positions_active from current open trades.
func (pm *PositionManager) SyncTick(ctx context.Context) {
	now := pm.nowFn()
	trades, err := pm.db.ListOpenTradesForSync(ctx)
	if err != nil {
		pm.log.Error().Err(err).Msg("position.sync.tick: list open trades failed")
		metrics.PositionSyncRunsTotal.WithLabelValues("error").Inc()
		return
	}
	if len(trades) == 0 {
		metrics.PositionSyncRunsTotal.WithLabelValues("empty").Inc()
		pm.rebuildRedisZset(ctx, nil)
		return
	}

	positions, err := pm.bc.GetPositionRisk(ctx, "")
	if err != nil {
		pm.log.Error().Err(err).Int("open_trades", len(trades)).Msg("position.sync.tick: positionRisk failed")
		metrics.PositionSyncRunsTotal.WithLabelValues("error").Inc()
		return
	}

	positionsBySymbol := make(map[string]binance.PositionRisk, len(positions))
	for _, p := range positions {
		positionsBySymbol[p.Symbol] = p
	}

	// Round 4 reconcile: track DB-side symbols to find binance-only positions
	// (positions on exchange we don't know about).
	localSymbols := make(map[string]bool, len(trades))
	for _, t := range trades {
		localSymbols[t.Symbol] = true
	}

	syncedOK, drift, marginCalls := 0, 0, 0
	for _, t := range trades {
		l := pm.log.With().Int64("trade_id", t.ID).Str("symbol", t.Symbol).Logger()

		pos, ok := positionsBySymbol[t.Symbol]
		if !ok {
			// v0.2 Step 5 + Round R.4 (F1): race-window defense. The Algo polling
			// collector (1min cron) is supposed to catch FINISHED algos and
			// auto-close the trade BEFORE we get here. But robfig/cron v3 doesn't
			// guarantee execution order within the same minute, so we may observe
			// the trade still 'open' while the Algo has actually FINISHED.
			//
			// Round R.4: try EVERY non-nil algo (trail first — fires far more
			// often than disaster on profitable trades). Pre-fix the check only
			// looked at disaster_stop_id, so trail-fired closes (mu's INJ #66 /
			// TURBOUSDT #67 / ESPORTSUSDT #59) tripped false halts.
			if pm.algoReconciler != nil {
				entryTs := time.Time{}
				if t.EntryTs.Valid {
					entryTs = t.EntryTs.Time
				}
				entry := decimalFromPgNumeric(t.EntryPrice)
				qty := decimalFromPgNumeric(t.CurrentQty)
				algoCandidates := []struct {
					id   string
					kind string
				}{
					{t.BinanceTrailAlgoID.String, "trail"},
					{t.BinanceDisasterStopOrderID.String, "disaster"},
				}
				resolved := false
				for _, c := range algoCandidates {
					if c.id == "" {
						continue
					}
					if pm.algoReconciler.TryReconcile(ctx, t.ID, t.Symbol, c.id, entry, qty, entryTs) {
						l.Info().Str("via_algo", c.kind).Msg("position.local_only_orphan: algo_reconciler resolved (FINISHED auto-close), skipping halt")
						metrics.PositionMarginRatio.DeleteLabelValues(t.Symbol)
						resolved = true
						break
					}
				}
				if resolved {
					continue
				}
			}
			// Round 4 local_only_orphan: DB has open trade, Binance doesn't,
			// AND Algo (if any) is not FINISHED. Possible causes: Algo
			// CANCELED/EXPIRED mid-flight, mu manually closed in Binance UI,
			// or genuine state divergence. Trip halt + write RCA.
			l.Error().Msg("position.local_only_orphan: open trade missing from Binance")
			metrics.PositionLocalOnlyOrphanTotal.Inc()
			metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "missing").Inc()
			// v0.2 Catch 2: clear stale margin_ratio gauge — orphan branch
			// `continue`s before the healthy Set() at line ~219, so without
			// this delete the gauge keeps its last value for hours/days while
			// trader-app stays up. Verified by trade 49 BUSDT in Round 8.
			metrics.PositionMarginRatio.DeleteLabelValues(t.Symbol)
			pm.tripReconcileHalt(ctx, haltTypeLocalOrphan, map[string]any{
				"trade_id":    t.ID,
				"symbol":      t.Symbol,
				"signal_id":   t.SignalID.Int64,
				"db_status":   "open",
				"binance":     "missing",
				"detected_at": now.Format(time.RFC3339),
			}, l)
			drift++
			continue
		}

		// Direction check (LONG = positionAmt > 0 in one-way mode).
		isLong := pos.PositionAmt.IsPositive()
		expectedLong := t.Direction == "LONG"
		if isLong != expectedLong {
			l.Warn().
				Str("expected", t.Direction).
				Str("actual_amt", pos.PositionAmt.String()).
				Msg("position.sync.drift: direction mismatch")
			metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "direction").Inc()
			// Direction mismatch is a hard divergence — always halt.
			pm.tripReconcileHalt(ctx, haltTypeDriftExceed, map[string]any{
				"trade_id":   t.ID,
				"symbol":     t.Symbol,
				"drift_type": "direction",
				"db_dir":     t.Direction,
				"binance_amt": pos.PositionAmt.String(),
			}, l)
			drift++
		}

		// Qty drift: compare abs(positionAmt) to DB current_qty (or notional/entry if state empty).
		absAmt := pos.PositionAmt.Abs()
		if t.CurrentQty.Valid {
			dbQty := decimalFromPgNumeric(t.CurrentQty)
			if !dbQty.IsZero() {
				deviation := dbQty.Sub(absAmt).Abs().Div(dbQty)
				if deviation.GreaterThan(decimal.NewFromFloat(driftHaltThresholdPct)) {
					// > 5% qty drift → halt + RCA (Round 4 escalation).
					l.Error().Str("db_qty", dbQty.String()).Str("binance_qty", absAmt.String()).
						Str("deviation_pct", deviation.String()).
						Msg("position.drift_halt: qty mismatch > 5%")
					metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "qty").Inc()
					metrics.PositionDriftHaltTotal.WithLabelValues(t.Symbol, "qty").Inc()
					pm.tripReconcileHalt(ctx, haltTypeDriftExceed, map[string]any{
						"trade_id":      t.ID,
						"symbol":        t.Symbol,
						"drift_type":    "qty",
						"db_qty":        dbQty.String(),
						"binance_qty":   absAmt.String(),
						"deviation_pct": deviation.String(),
						"threshold_pct": driftHaltThresholdPct,
					}, l)
					drift++
				} else if deviation.GreaterThan(decimal.NewFromFloat(0.01)) {
					// 1-5% drift: log only (noise tolerance from Round 3).
					l.Warn().Str("db_qty", dbQty.String()).Str("binance_qty", absAmt.String()).
						Str("deviation_pct", deviation.String()).
						Msg("position.sync.drift: qty mismatch > 1% (no halt)")
					metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "qty").Inc()
				}
			}
		}

		// Update position_states with fresh data.
		if err := pm.db.UpdatePositionStateSync(ctx, gen.UpdatePositionStateSyncParams{
			TradeID:      t.ID,
			CurrentQty:   absAmt,
			HighestPrice: pos.MarkPrice,
			LastCheckTs:  now,
		}); err != nil {
			l.Error().Err(err).Msg("position.sync: update position_state failed")
			continue
		}

		// MARGIN_CALL check: margin_ratio = (-unrealized_pnl) / margin (only when underwater).
		var marginRatio decimal.Decimal
		if t.Margin.IsPositive() {
			marginRatio = pos.UnrealizedProfit.Neg().Div(t.Margin)
		}
		marginRatioF, _ := marginRatio.Float64()
		metrics.PositionMarginRatio.WithLabelValues(t.Symbol).Set(marginRatioF)

		if marginRatio.GreaterThan(decimal.NewFromFloat(marginCallRatioTrigger)) {
			l.Warn().
				Str("margin_ratio", marginRatio.String()).
				Str("unrealized_pnl", pos.UnrealizedProfit.String()).
				Msg("position.margin_call: triggering emergency exit")
			metrics.MarginCallTriggeredTotal.WithLabelValues(t.Symbol).Inc()
			pm.emergencyExit(ctx, t.ID, t.Symbol, absAmt, pos.MarkPrice, "margin_call", l)
			marginCalls++
		}

		syncedOK++
	}

	// Round 4 binance_only_unknown: scan Binance positions for symbols not in
	// our open-trades set. Means mu / another process opened a position outside
	// the trader. Halt + RCA — do NOT auto-close (could be intentional).
	unknown := 0
	for _, pos := range positions {
		if localSymbols[pos.Symbol] {
			continue
		}
		pm.log.Error().Str("symbol", pos.Symbol).Str("position_amt", pos.PositionAmt.String()).
			Msg("position.binance_only_unknown: position not in DB")
		metrics.PositionBinanceOnlyUnknownTotal.Inc()
		pm.tripReconcileHalt(ctx, haltTypeBinanceOnly, map[string]any{
			"symbol":       pos.Symbol,
			"position_amt": pos.PositionAmt.String(),
			"mark_price":   pos.MarkPrice.String(),
			"entry_price":  pos.EntryPrice.String(),
			"detected_at":  now.Format(time.RFC3339),
		}, pm.log)
		unknown++
	}

	pm.rebuildRedisZset(ctx, trades)

	pm.log.Info().
		Int("open_trades", len(trades)).
		Int("binance_positions", len(positions)).
		Int("synced_ok", syncedOK).
		Int("drift", drift).
		Int("margin_calls", marginCalls).
		Int("binance_only_unknown", unknown).
		Msg("position.sync.tick")

	if drift > 0 || unknown > 0 {
		metrics.PositionSyncRunsTotal.WithLabelValues("drift").Inc()
	} else {
		metrics.PositionSyncRunsTotal.WithLabelValues("ok").Inc()
	}
}

// tripReconcileHalt trips the circuit breaker for a reconcile event (orphan /
// unknown / drift > 5%) AND writes a halt_rca row with full context.
// All failures are logged but non-fatal: sync tick continues so other halts
// + position updates aren't lost. halt_until = NOW + 1h (auto-reset by
// existing filters.maintainHaltState path).
func (pm *PositionManager) tripReconcileHalt(ctx context.Context, haltType string, context map[string]any, log zerolog.Logger) {
	now := pm.nowFn()
	haltReason := "position_" + haltType // e.g. position_local_only_orphan
	haltUntil := pgtype.Timestamptz{Time: now.Add(reconcileHaltDuration), Valid: true}

	if err := pm.db.TripGenericHalt(ctx, gen.TripGenericHaltParams{
		HaltReason: pgtype.Text{String: haltReason, Valid: true},
		HaltUntil:  haltUntil,
	}); err != nil {
		log.Error().Err(err).Str("halt_type", haltType).Msg("reconcile.halt: trip failed")
		return
	}

	ctxJSON, err := json.Marshal(context)
	if err != nil {
		log.Error().Err(err).Msg("reconcile.halt: context json marshal failed")
		ctxJSON = []byte(`{}`)
	}
	rca, err := pm.db.InsertHaltRCA(ctx, gen.InsertHaltRCAParams{
		HaltType:    haltType,
		ContextJson: ctxJSON,
	})
	if err != nil {
		log.Error().Err(err).Str("halt_type", haltType).Msg("reconcile.halt: rca write failed")
		return
	}
	metrics.HaltRCAPendingTotal.WithLabelValues(haltType).Inc()
	log.Warn().
		Int64("halt_rca_id", rca.ID).
		Str("halt_type", haltType).
		Time("halt_until", haltUntil.Time).
		Msg("halt.rca.created")
}

// rebuildRedisZset replaces positions_active zset to match `trades`. Member=trade_id (str),
// score=entered_at unix. Robust to drift (no event-driven add/remove tracking).
// trades=nil empties the zset (used when no open positions exist).
func (pm *PositionManager) rebuildRedisZset(ctx context.Context, trades []gen.ListOpenTradesForSyncRow) {
	pipe := pm.rdb.TxPipeline()
	pipe.Del(ctx, redisKeyPositionsActive)
	for _, t := range trades {
		if !t.EntryTs.Valid {
			continue
		}
		pipe.ZAdd(ctx, redisKeyPositionsActive, redis.Z{
			Score:  float64(t.EntryTs.Time.Unix()),
			Member: strconv.FormatInt(t.ID, 10),
		})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		pm.log.Error().Err(err).Msg("position.sync: redis zset rebuild failed (non-fatal)")
	}
}

// emergencyExit is invoked when margin_call triggers. Places a MARKET SELL
// reduceOnly to close the entire position, marks trade failed, records exit.
// Failure of any step is logged but does NOT bubble (Round 3 v0.1 keeps it
// best-effort; ops alerts via metrics).
func (pm *PositionManager) emergencyExit(ctx context.Context, tradeID int64, symbol string, qty, approxPrice decimal.Decimal, reason string, log zerolog.Logger) {
	clientOrderID := fmt.Sprintf("emrg_%d_%d", tradeID, pm.nowFn().Unix())
	res, err := pm.bc.PlaceMarketOrder(ctx, symbol, "SELL", qty.String(), clientOrderID)
	if err != nil {
		log.Error().Err(err).Msg("position.emergency_exit: SELL failed (position may still be open)")
		return
	}
	closePrice := res.AvgPrice
	if closePrice.IsZero() {
		closePrice = approxPrice
	}
	log.Info().
		Int64("order_id", res.OrderID).
		Str("close_price", closePrice.String()).
		Str("reason", reason).
		Msg("position.emergency_exit: filled")

	if err := pm.db.InsertTradeExit(ctx, gen.InsertTradeExitParams{
		TradeID: pgtype.Int8{Int64: tradeID, Valid: true},
		Ts:      pm.nowFn(),
		Type:    reason,
		Qty:     qty,
		Price:   closePrice,
		Pnl:     decimal.Zero, // computed by Round 5+ pnl reconciliation
	}); err != nil {
		log.Error().Err(err).Msg("position.emergency_exit: InsertTradeExit failed")
	}
	if err := pm.db.UpdateTradeFailed(ctx, gen.UpdateTradeFailedParams{
		ID:         tradeID,
		ExitReason: pgtype.Text{String: reason, Valid: true},
		ExitTs:     pgtype.Timestamptz{Time: pm.nowFn(), Valid: true},
	}); err != nil {
		log.Error().Err(err).Msg("position.emergency_exit: UpdateTradeFailed failed")
	}

	// v0.2 Catch 5 + Catch 2: mirror exit_manager.persistClose terminal cleanup.
	// Before this fix, emergency closes left position_states + zset members +
	// margin_ratio gauge orphaned (only the regular close pipeline cleaned
	// them). All non-fatal — trades.status='failed' is the authoritative state.
	if err := pm.db.DeletePositionState(ctx, tradeID); err != nil {
		log.Warn().Err(err).Msg("position.emergency_exit: DeletePositionState failed (non-fatal)")
	}
	if err := pm.rdb.ZRem(ctx, redisKeyPositionsActive, strconv.FormatInt(tradeID, 10)).Err(); err != nil {
		log.Warn().Err(err).Msg("position.emergency_exit: ZREM positions_active failed (non-fatal, next sync rebuilds)")
	}
	metrics.PositionMarginRatio.DeleteLabelValues(symbol)
}

// decimalFromPgNumeric converts pgtype.Numeric → decimal.Decimal. Zero on Valid=false.
func decimalFromPgNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid {
		return decimal.Zero
	}
	v, err := n.Value()
	if err != nil || v == nil {
		return decimal.Zero
	}
	s, ok := v.(string)
	if !ok {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
