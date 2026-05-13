// v0.2 Gap 1: Algo polling reconciler. Runs every 1min, queries each open
// trade's disaster-stop Algo by algoId, and auto-closes the trade when
// algoStatus=FINISHED (Algo triggered + filled by Binance).
//
// Why this exists: pre-v0.2, Algo TRIGGERED on Binance only surfaced to the
// trader via position_manager's local_only_orphan path (DB says open, Binance
// shows no position). That path tripped a halt + required mu to manually
// reconcile each event (Round 8 §4.5 natural reproduction). The polling
// reconciler closes the trade automatically so the orphan branch only fires
// for genuinely abnormal cases.
//
// Design notes:
//   - Status logic is in this package (not the binance package) to keep the
//     binance client thin. AlgoOrderQuery shape mirrors what binance returns.
//   - Coordination with Round 4 reconcile: this tick runs at the same 1min
//     cadence as position_manager. The 段 2b position_manager defensive
//     check (separate response) queries the reconciler before tripping
//     local_only_orphan halt so cron-ordering races don't trigger false halts.
//   - realized_pnl LONG-only formula matches exit_manager.persistClose:
//     (actualPrice - entry_price) × qty. fees=0 placeholder (Round 5 v0.1
//     same shape; v0.3+ may fetch userTrades).
//   - All terminal cleanup mirrors exit_manager.persistClose:
//     InsertTradeExit + UpdateTradeClosed + DeletePositionState + ZREM +
//     UpdateAfterTradeClose + DeleteLabelValues margin_ratio gauge.

package execution

import (
	"context"
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

// AlgoReconcilerDeps is the minimal DB surface the reconciler needs.
type AlgoReconcilerDeps interface {
	ListOpenTradesWithAlgo(ctx context.Context) ([]gen.ListOpenTradesWithAlgoRow, error)
	// InsertTradeExitIdempotent returns (1, nil) on insert, (0, nil) on duplicate.
	// The idempotent variant is required here because ReconcileTick and TryReconcile
	// can both call autoCloseFromFields for the same trade concurrently.
	InsertTradeExitIdempotent(ctx context.Context, arg gen.InsertTradeExitParams) (int64, error)
	UpdateTradeClosed(ctx context.Context, arg gen.UpdateTradeClosedParams) error
	DeletePositionState(ctx context.Context, tradeID int64) error
	UpdateAfterTradeClose(ctx context.Context, arg gen.UpdateAfterTradeCloseParams) error
	// v0.2 Round 2 Module A: partial-close path (TP1/TP2). Does NOT close the trade,
	// only decrements position_states.current_qty and clears the matching tpN algo id.
	DecrementPositionQty(ctx context.Context, arg gen.DecrementPositionQtyParams) error
	ClearTPAlgoID(ctx context.Context, arg gen.ClearTPAlgoIDParams) error
	UpdateDailyPnlPartial(ctx context.Context, arg gen.UpdateDailyPnlPartialParams) error
}

// AlgoQuerier is the minimal binance surface.
type AlgoQuerier interface {
	QueryAlgoOrder(ctx context.Context, algoID int64) (binance.AlgoOrderQuery, error)
}

// AlgoReconciler runs the 1min poll loop + auto-close logic.
type AlgoReconciler struct {
	db    AlgoReconcilerDeps
	bc    AlgoQuerier
	ff    FeesFetcher // optional; nil → fees=0
	rdb   *redis.Client
	log   zerolog.Logger
	nowFn func() time.Time
}

func NewAlgoReconciler(db AlgoReconcilerDeps, bc AlgoQuerier, rdb *redis.Client, log zerolog.Logger) *AlgoReconciler {
	return &AlgoReconciler{db: db, bc: bc, rdb: rdb, log: log, nowFn: timez.NowUTC}
}

// WithFeesFetcher wires the real commission fetcher (call after NewAlgoReconciler in main).
func (ar *AlgoReconciler) WithFeesFetcher(ff FeesFetcher) *AlgoReconciler {
	ar.ff = ff
	return ar
}

// sumAlgoFees fetches commission for the SELL order that Binance placed when
// the Algo triggered. actualOrderID is the string from AlgoOrderQuery.ActualOrderID.
// Non-fatal: logs warning, returns decimal.Zero on any error.
func (ar *AlgoReconciler) sumAlgoFees(ctx context.Context, symbol, actualOrderID string, log zerolog.Logger) decimal.Decimal {
	if ar.ff == nil || actualOrderID == "" {
		return decimal.Zero
	}
	orderID, err := strconv.ParseInt(actualOrderID, 10, 64)
	if err != nil {
		log.Warn().Err(err).Str("actual_order_id", actualOrderID).Msg("algo.triggered: parse ActualOrderID failed, fees=0")
		return decimal.Zero
	}
	fills, err := ar.ff.GetUserTrades(ctx, symbol, orderID)
	if err != nil {
		log.Warn().Err(err).Int64("order_id", orderID).Msg("algo.triggered: GetUserTrades failed, fees=0")
		return decimal.Zero
	}
	total := decimal.Zero
	for _, f := range fills {
		total = total.Add(f.Commission)
	}
	return total
}

// ReconcileTick polls every open trade with an Algo and auto-closes the ones
// where Binance reports algoStatus=FINISHED. Per-trade errors are logged but
// don't block subsequent rows (best-effort, retries next tick).
//
// Round 1.x + Round 2: each row may carry up to 4 algos (disaster_stop / trail
// / tp1 / tp2). TPs are polled FIRST so any partial close decrements the
// running currentQty before disaster/trail full-close compute realized_pnl.
//   Same-tick TP1 + Trail FINISHED: TP1 partial close (0.2Q) → running=0.8Q
//   → Trail full close uses 0.8Q for pnl. Correct.
func (ar *AlgoReconciler) ReconcileTick(ctx context.Context) {
	rows, err := ar.db.ListOpenTradesWithAlgo(ctx)
	if err != nil {
		ar.log.Error().Err(err).Msg("algo.polling.tick: list open trades failed")
		metrics.AlgoPollingRunsTotal.WithLabelValues("err").Inc()
		return
	}
	if len(rows) == 0 {
		metrics.AlgoPollingRunsTotal.WithLabelValues("empty").Inc()
		return
	}

	triggered := 0
	partials := 0
	for _, r := range rows {
		currentQty := decimalFromPgNumeric(r.CurrentQty)

		// 1. TP1 partial poll (decrements running currentQty on FINISHED).
		if r.BinanceTP1AlgoID.Valid && r.BinanceTP1AlgoID.String != "" {
			if soldQty, ok := ar.pollOnePartial(ctx, r, r.BinanceTP1AlgoID.String, ExitReasonTP1); ok {
				currentQty = currentQty.Sub(soldQty)
				if currentQty.IsNegative() {
					currentQty = decimal.Zero
				}
				partials++
			}
		}
		// 2. TP2 partial poll.
		if r.BinanceTP2AlgoID.Valid && r.BinanceTP2AlgoID.String != "" {
			if soldQty, ok := ar.pollOnePartial(ctx, r, r.BinanceTP2AlgoID.String, ExitReasonTP2); ok {
				currentQty = currentQty.Sub(soldQty)
				if currentQty.IsNegative() {
					currentQty = decimal.Zero
				}
				partials++
			}
		}
		// 3. Disaster stop full close (use running currentQty).
		if r.BinanceDisasterStopOrderID.Valid && r.BinanceDisasterStopOrderID.String != "" {
			if ar.pollOneFull(ctx, r, r.BinanceDisasterStopOrderID.String, ExitReasonDisaster, "disaster", currentQty) {
				triggered++
				continue // trade closed; skip trail
			}
		}
		// 4. Trail full close (mutually exclusive with disaster in practice — reduceOnly).
		if r.BinanceTrailAlgoID.Valid && r.BinanceTrailAlgoID.String != "" {
			reason := trailExitReasonForStage(r.TrailStage)
			if ar.pollOneFull(ctx, r, r.BinanceTrailAlgoID.String, reason, "trail", currentQty) {
				triggered++
			}
		}
	}

	ar.log.Info().
		Int("open_trades", len(rows)).
		Int("triggered", triggered).
		Int("partials", partials).
		Msg("algo.polling.tick")
	metrics.AlgoPollingRunsTotal.WithLabelValues("ok").Inc()
}

// trailExitReasonForStage maps trail_stage (1-4) to its exit_reason string.
// Stage 0 shouldn't have a trail algo, but treat as S1 defensively.
func trailExitReasonForStage(stage int16) string {
	switch stage {
	case 2:
		return ExitReasonTrailS2
	case 3:
		return ExitReasonTrailS3
	case 4:
		return ExitReasonTrailS4
	default:
		return ExitReasonTrailS1
	}
}

// pollOneFull queries one algo and on FINISHED runs the full-close pipeline
// using the running currentQty (which may already be decremented by TPs this tick).
// Returns true if the trade was closed.
func (ar *AlgoReconciler) pollOneFull(ctx context.Context, r gen.ListOpenTradesWithAlgoRow, algoIDStr, exitReason, kind string, runningQty decimal.Decimal) bool {
	q, ok := ar.queryAlgo(ctx, r.ID, r.Symbol, algoIDStr, kind, exitReason)
	if !ok {
		return false
	}
	if q.AlgoStatus != "FINISHED" {
		return false
	}
	l := ar.logFor(r.ID, r.Symbol, q.AlgoID, kind, exitReason)
	return ar.autoClose(ctx, r, q, exitReason, runningQty, l)
}

// pollOnePartial queries a TP algo and on FINISHED runs the partial-close pipeline.
// Returns (sold_qty, true) on successful partial close. Caller decrements its
// running currentQty by sold_qty for the subsequent full-close polls.
func (ar *AlgoReconciler) pollOnePartial(ctx context.Context, r gen.ListOpenTradesWithAlgoRow, algoIDStr, exitReason string) (decimal.Decimal, bool) {
	q, ok := ar.queryAlgo(ctx, r.ID, r.Symbol, algoIDStr, "tp", exitReason)
	if !ok {
		return decimal.Zero, false
	}
	if q.AlgoStatus != "FINISHED" {
		return decimal.Zero, false
	}
	l := ar.logFor(r.ID, r.Symbol, q.AlgoID, "tp", exitReason)
	return ar.partialClose(ctx, r, q, exitReason, l)
}

// queryAlgo wraps QueryAlgoOrder + CANCELED/EXPIRED handling shared by both poll variants.
// Returns (query, true) when status is actionable (caller should check FINISHED separately).
// Returns (zero, false) on parse error, query error, CANCELED, EXPIRED, or unknown status.
func (ar *AlgoReconciler) queryAlgo(ctx context.Context, tradeID int64, symbol, algoIDStr, kind, exitReason string) (binance.AlgoOrderQuery, bool) {
	algoID, err := strconv.ParseInt(algoIDStr, 10, 64)
	if err != nil {
		ar.log.Warn().Err(err).Int64("trade_id", tradeID).
			Str("algo_id_raw", algoIDStr).Str("kind", kind).
			Msg("algo.polling: invalid algo_id, skipping")
		return binance.AlgoOrderQuery{}, false
	}
	l := ar.logFor(tradeID, symbol, algoID, kind, exitReason)
	q, err := ar.bc.QueryAlgoOrder(ctx, algoID)
	if err != nil {
		l.Warn().Err(err).Msg("algo.polling: query failed (will retry next tick)")
		return binance.AlgoOrderQuery{}, false
	}
	switch q.AlgoStatus {
	case "WORKING", "NEW", "FINISHED":
		// NEW = armed, waiting for trigger condition (semantically same as WORKING
		// for our poll path: caller checks FINISHED separately, NEW falls through).
		return q, true
	case "CANCELED", "EXPIRED":
		// Trader upgrade (trail S1→S2) cancels old algo + rewrites algo_id atomically.
		// Mid-upgrade race or external cancel: next tick resolves naturally.
		l.Info().Str("algo_status", q.AlgoStatus).Msg("algo.polling: algo gone (trader upgrade or external cancel)")
		return binance.AlgoOrderQuery{}, false
	default:
		l.Warn().Str("algo_status", q.AlgoStatus).Msg("algo.polling: unknown status, treating as WORKING")
		return binance.AlgoOrderQuery{}, false
	}
}

func (ar *AlgoReconciler) logFor(tradeID int64, symbol string, algoID int64, kind, exitReason string) zerolog.Logger {
	return ar.log.With().Int64("trade_id", tradeID).Str("symbol", symbol).
		Int64("algo_id", algoID).Str("kind", kind).Str("exit_reason", exitReason).Logger()
}

// TryReconcile is the single-trade public entry point used by
// position_manager.SyncTick local_only_orphan defensive check (v0.2 Step 5).
// Queries the Algo status; if FINISHED, performs the same auto-close as
// ReconcileTick with the caller-provided exit_reason. Returns true on
// successful reconcile (caller skips the orphan halt).
//
// Round R.5 (Bug C): exit_reason is now an explicit parameter so the trail
// path doesn't silently log as 'disaster'. Pre-fix every trail-fired close
// that flowed through position_manager's race-window defense got recorded
// as type='disaster' in trade_exits, which double-counted PnL in the rare
// case algo_reconciler ALSO recorded the real trail_sN row (mu 真盘
// COSUSDT #70: +$8.91 spurious entry).
//
// Caller passes the trade fields rather than a row type so this works from
// BOTH ListOpenTradesWithAlgoRow (proactive sweep) and ListOpenTradesForSyncRow
// (position_manager race-window defense).
func (ar *AlgoReconciler) TryReconcile(ctx context.Context, tradeID int64, symbol, algoIDStr, exitReason string, entryPrice, currentQty decimal.Decimal, entryTs time.Time) bool {
	if algoIDStr == "" {
		return false
	}
	if exitReason == "" {
		exitReason = ExitReasonDisaster // safety default for legacy callers
	}
	algoID, err := strconv.ParseInt(algoIDStr, 10, 64)
	if err != nil {
		ar.log.Warn().Err(err).Int64("trade_id", tradeID).Str("algo_id_raw", algoIDStr).
			Msg("algo.try_reconcile: invalid algo_id, skipping")
		return false
	}
	l := ar.log.With().Int64("trade_id", tradeID).Str("symbol", symbol).Int64("algo_id", algoID).Str("exit_reason", exitReason).Logger()
	q, err := ar.bc.QueryAlgoOrder(ctx, algoID)
	if err != nil {
		l.Warn().Err(err).Msg("algo.try_reconcile: query failed")
		return false
	}
	if q.AlgoStatus != "FINISHED" {
		return false
	}
	return ar.autoCloseFromFields(ctx, tradeID, symbol, q, entryPrice, currentQty, entryTs, exitReason, l)
}

// HasFinishedTPForTrade is the Round R.5 Bug B defense: checks whether any
// TP algo for the trade is FINISHED on Binance. Used by position_manager
// before tripping drift_exceeded halt — if a TP just fired, the Binance qty
// dropped while DB.current_qty is still pre-TP (algo_reconciler will
// reconcile this same tick or next). Returns true when at least one TP is
// FINISHED; caller should skip the halt and let algo_reconciler decrement.
//
// Two API calls worst case (TP1 + TP2). Errors return false (don't gate
// the halt on transient errors).
func (ar *AlgoReconciler) HasFinishedTPForTrade(ctx context.Context, tp1AlgoIDStr, tp2AlgoIDStr string) bool {
	for _, idStr := range []string{tp1AlgoIDStr, tp2AlgoIDStr} {
		if idStr == "" {
			continue
		}
		algoID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		q, err := ar.bc.QueryAlgoOrder(ctx, algoID)
		if err != nil {
			continue
		}
		if q.AlgoStatus == "FINISHED" {
			return true
		}
	}
	return false
}

// autoClose adapts the row-based call site to the primitive helper, passing
// the running currentQty (may differ from r.CurrentQty after TP partial closes
// in the same tick).
func (ar *AlgoReconciler) autoClose(ctx context.Context, r gen.ListOpenTradesWithAlgoRow, q binance.AlgoOrderQuery, exitReason string, runningQty decimal.Decimal, log zerolog.Logger) bool {
	entryTs := time.Time{}
	if r.EntryTs.Valid {
		entryTs = r.EntryTs.Time
	}
	return ar.autoCloseFromFields(ctx, r.ID, r.Symbol, q,
		decimalFromPgNumeric(r.EntryPrice),
		runningQty,
		entryTs, exitReason, log)
}

// partialClose handles a TP1/TP2 FINISHED: insert exit row, decrement position
// qty, clear the matching tpN algo_id, accumulate daily_pnl (no consec_losses).
// Returns (soldQty, true) on success; soldQty drives the caller's running
// currentQty so subsequent disaster/trail full-close uses correct qty.
func (ar *AlgoReconciler) partialClose(ctx context.Context, r gen.ListOpenTradesWithAlgoRow, q binance.AlgoOrderQuery, exitReason string, log zerolog.Logger) (decimal.Decimal, bool) {
	now := ar.nowFn()
	closePrice := q.ActualPrice
	if closePrice.IsZero() {
		closePrice = q.TriggerPrice
	}
	if closePrice.IsZero() {
		log.Error().Msg("tp.triggered: both actualPrice and triggerPrice zero, skipping (next tick may resolve)")
		return decimal.Zero, false
	}
	// Algo qty is the partial size we armed (e.g. 20% of entry qty). Binance fills
	// exactly this amount when triggered (reduceOnly capped by remaining position).
	qty := q.Quantity
	if qty.IsZero() {
		log.Error().Msg("tp.triggered: Algo.Quantity zero, cannot record partial close")
		return decimal.Zero, false
	}
	entry := decimalFromPgNumeric(r.EntryPrice)
	realizedPnl := closePrice.Sub(entry).Mul(qty) // LONG only

	n, err := ar.db.InsertTradeExitIdempotent(ctx, gen.InsertTradeExitParams{
		TradeID: pgtype.Int8{Int64: r.ID, Valid: true},
		Ts:      now,
		Type:    exitReason,
		Qty:     qty,
		Price:   closePrice,
		Pnl:     realizedPnl,
	})
	if err != nil {
		log.Error().Err(err).Msg("tp.triggered: InsertTradeExit failed (next tick retries)")
		return decimal.Zero, false
	}
	if n == 0 {
		// Already recorded last tick (idempotent skip). Don't touch position_states
		// again — qty was already decremented. Caller should NOT subtract qty either.
		log.Info().Msg("tp.triggered: duplicate skipped (idempotent)")
		return decimal.Zero, false
	}

	// Real commission (Binance SELL order fee).
	fees := ar.sumAlgoFees(ctx, r.Symbol, q.ActualOrderID, log)
	// daily_pnl: net of fees (mirror autoCloseFromFields accounting).
	dailyDelta := realizedPnl.Sub(fees)

	if err := ar.db.DecrementPositionQty(ctx, gen.DecrementPositionQtyParams{
		TradeID:     r.ID,
		Delta:       qty,
		LastCheckTs: now,
	}); err != nil {
		log.Error().Err(err).Msg("tp.triggered: DecrementPositionQty failed (trail_upgrader will use stale qty)")
	}
	if err := ar.db.ClearTPAlgoID(ctx, gen.ClearTPAlgoIDParams{ID: r.ID, Type: exitReason}); err != nil {
		log.Warn().Err(err).Msg("tp.triggered: ClearTPAlgoID failed (next tick will re-poll FINISHED, idempotent skip)")
	}
	bjt := now.In(timez.BJT)
	pgDate := pgtype.Date{Valid: true}
	_ = pgDate.Scan(bjt.Format("2006-01-02"))
	if err := ar.db.UpdateDailyPnlPartial(ctx, gen.UpdateDailyPnlPartialParams{
		RealizedPnl:  dailyDelta,
		DailyPnlDate: pgDate,
	}); err != nil {
		log.Warn().Err(err).Msg("tp.triggered: UpdateDailyPnlPartial failed (Round 6 may read stale daily_pnl)")
	}

	// Realized PnL counter (TP partial typically positive, but use sign accounting for safety).
	sign := "positive"
	if realizedPnl.IsNegative() {
		sign = "negative"
	} else if realizedPnl.IsZero() {
		sign = "zero"
	}
	metrics.RealizedPnlTotal.WithLabelValues(r.Symbol, sign).Add(mustFloat(realizedPnl.Abs()))
	metrics.TPFilledTotal.WithLabelValues(r.Symbol, exitReason).Inc()

	log.Info().
		Str("close_price", closePrice.String()).
		Str("qty", qty.String()).
		Str("realized_pnl", realizedPnl.String()).
		Str("fees", fees.String()).
		Msg("tp.triggered.partial_close")
	return qty, true
}

// autoCloseFromFields is the shared close pipeline taking primitive inputs.
// Mirrors exit_manager.persistClose so the close artifacts look identical to
// a soft/hard timeout exit, just with exit_reason from the caller (disaster
// for STOP_MARKET / trail_sN for TRAILING_STOP_MARKET) and price from
// Algo.actualPrice.
func (ar *AlgoReconciler) autoCloseFromFields(ctx context.Context, tradeID int64, symbol string, q binance.AlgoOrderQuery, entryPrice, currentQty decimal.Decimal, entryTs time.Time, exitReason string, log zerolog.Logger) bool {
	now := ar.nowFn()
	// Use Algo.actualPrice (Binance authoritative fill price). Fallback to
	// triggerPrice if actualPrice is somehow 0 (shouldn't happen for FINISHED
	// but be defensive — exit_reason='disaster' still reflects intent).
	closePrice := q.ActualPrice
	if closePrice.IsZero() {
		closePrice = q.TriggerPrice
		log.Warn().Msg("algo.triggered: actualPrice zero, using triggerPrice (Binance response anomaly)")
	}
	if closePrice.IsZero() {
		log.Error().Msg("algo.triggered: both actualPrice and triggerPrice zero, skipping (next tick may resolve)")
		return false
	}

	// Quantity preference: position_states.current_qty (DB authoritative for
	// what we believed was open). Fallback to Algo.quantity (what we originally
	// armed the Algo with). They should match for a clean LONG entry.
	qty := currentQty
	if qty.IsZero() {
		qty = q.Quantity
	}
	if qty.IsZero() {
		log.Error().Msg("algo.triggered: both DB qty and Algo qty zero, cannot compute pnl")
		return false
	}

	// Fetch real exit commission. ActualOrderID is the SELL order Binance placed
	// when the Algo triggered. Entry fees not captured yet (no entry_order_id in DB).
	fees := ar.sumAlgoFees(ctx, symbol, q.ActualOrderID, log)
	realizedPnl := closePrice.Sub(entryPrice).Mul(qty) // LONG only

	n, err := ar.db.InsertTradeExitIdempotent(ctx, gen.InsertTradeExitParams{
		TradeID: pgtype.Int8{Int64: tradeID, Valid: true},
		Ts:      now,
		Type:    exitReason,
		Qty:     qty,
		Price:   closePrice,
		Pnl:     realizedPnl,
	})
	if err != nil {
		log.Error().Err(err).Msg("algo.triggered: InsertTradeExit failed (next tick will retry)")
		return false
	}
	if n == 0 {
		// ON CONFLICT: another goroutine (ReconcileTick/TryReconcile) already closed
		// this trade. Skip UpdateTradeClosed + UpdateAfterTradeClose to prevent
		// duplicate circuit_breaker rollup (consecutive_losses double-increment).
		log.Info().Int64("trade_id", tradeID).Msg("algo.triggered: duplicate close skipped (idempotent)")
		return true
	}
	if err := ar.db.UpdateTradeClosed(ctx, gen.UpdateTradeClosedParams{
		ID:          tradeID,
		ExitTs:      pgtype.Timestamptz{Time: now, Valid: true},
		ExitPrice:   closePrice,
		ExitReason:  pgtype.Text{String: exitReason, Valid: true},
		RealizedPnl: realizedPnl,
		Fees:        fees,
	}); err != nil {
		log.Error().Err(err).Msg("algo.triggered: UpdateTradeClosed failed (CRITICAL — trade exit row written but status still open)")
		return false
	}
	if err := ar.db.DeletePositionState(ctx, tradeID); err != nil {
		log.Warn().Err(err).Msg("algo.triggered: DeletePositionState failed (non-fatal)")
	}
	if err := ar.rdb.ZRem(ctx, redisKeyPositionsActive, strconv.FormatInt(tradeID, 10)).Err(); err != nil {
		log.Warn().Err(err).Msg("algo.triggered: ZREM positions_active failed (non-fatal)")
	}
	// circuit_breaker rollup (daily_pnl + consec_losses).
	bjt := now.In(timez.BJT)
	pgDate := pgtype.Date{Valid: true}
	_ = pgDate.Scan(bjt.Format("2006-01-02"))
	if err := ar.db.UpdateAfterTradeClose(ctx, gen.UpdateAfterTradeCloseParams{
		RealizedPnl:  realizedPnl,
		DailyPnlDate: pgDate,
	}); err != nil {
		log.Warn().Err(err).Msg("algo.triggered: UpdateAfterTradeClose failed (Round 6 will read stale state)")
	}
	// margin_ratio gauge cleanup (v0.2 Catch 2).
	metrics.PositionMarginRatio.DeleteLabelValues(symbol)
	// realized PnL counter — same sign accounting as exit_manager.
	sign := "positive"
	if realizedPnl.IsNegative() {
		sign = "negative"
	} else if realizedPnl.IsZero() {
		sign = "zero"
	}
	// Counter.Add panics on negative; metric labels sign and adds |pnl|.
	metrics.RealizedPnlTotal.WithLabelValues(symbol, sign).Add(mustFloat(realizedPnl.Abs()))
	metrics.AlgoTriggeredTotal.WithLabelValues(symbol, exitReason).Inc()

	holdHours := 0.0
	if !entryTs.IsZero() {
		holdHours = now.Sub(entryTs).Hours()
	}
	metrics.PositionHoldDurationHours.WithLabelValues(exitReason).Observe(holdHours)

	log.Info().
		Str("close_price", closePrice.String()).
		Str("realized_pnl", realizedPnl.String()).
		Str("qty", qty.String()).
		Float64("hold_hours", holdHours).
		Time("trigger_time", q.TriggerTime).
		Msg("algo.triggered.auto_close")
	return true
}
