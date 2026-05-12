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
	for _, r := range rows {
		algoID, err := strconv.ParseInt(r.BinanceDisasterStopOrderID.String, 10, 64)
		if err != nil {
			ar.log.Warn().Err(err).Int64("trade_id", r.ID).
				Str("algo_id_raw", r.BinanceDisasterStopOrderID.String).
				Msg("algo.polling: invalid algo_id, skipping")
			continue
		}
		l := ar.log.With().Int64("trade_id", r.ID).Str("symbol", r.Symbol).Int64("algo_id", algoID).Logger()

		q, err := ar.bc.QueryAlgoOrder(ctx, algoID)
		if err != nil {
			l.Warn().Err(err).Msg("algo.polling: query failed (will retry next tick)")
			continue
		}
		switch q.AlgoStatus {
		case "WORKING":
			// Normal — algo armed waiting for trigger. No action.
		case "FINISHED":
			// Algo triggered + market SELL filled. Auto-close the trade.
			if ar.autoClose(ctx, r, q, l) {
				triggered++
			}
		case "CANCELED", "EXPIRED":
			// Algo gone but trade still 'open'. Could be:
			//   a) regular close pipeline cancelled it mid-flight (race with
			//      exit_manager.closePosition between cancel + UPDATE 'closing')
			//   b) manual cancel via UI
			// Either way the missing Algo is now mu's problem — position_manager
			// local_only_orphan path will flag it on the next 1min tick when
			// Binance also reports the position missing. Log and continue.
			l.Warn().Str("algo_status", q.AlgoStatus).
				Msg("algo.polling: algo gone but trade open (position_manager orphan path will detect)")
		default:
			l.Warn().Str("algo_status", q.AlgoStatus).Msg("algo.polling: unknown status, treating as WORKING")
		}
	}

	ar.log.Info().
		Int("open_trades", len(rows)).
		Int("triggered", triggered).
		Msg("algo.polling.tick")
	metrics.AlgoPollingRunsTotal.WithLabelValues("ok").Inc()
}

// TryReconcile is the single-trade public entry point used by
// position_manager.SyncTick local_only_orphan defensive check (v0.2 Step 5).
// Queries the Algo status; if FINISHED, performs the same auto-close as
// ReconcileTick. Returns true on successful reconcile (caller skips the
// orphan halt). Returns false on any other algoStatus, query error, or
// unsuccessful close — caller proceeds with its normal orphan handling.
//
// Caller passes the trade fields rather than a row type so this works from
// BOTH ListOpenTradesWithAlgoRow (proactive sweep) and ListOpenTradesForSyncRow
// (position_manager race-window defense).
func (ar *AlgoReconciler) TryReconcile(ctx context.Context, tradeID int64, symbol, algoIDStr string, entryPrice, currentQty decimal.Decimal, entryTs time.Time) bool {
	if algoIDStr == "" {
		return false
	}
	algoID, err := strconv.ParseInt(algoIDStr, 10, 64)
	if err != nil {
		ar.log.Warn().Err(err).Int64("trade_id", tradeID).Str("algo_id_raw", algoIDStr).
			Msg("algo.try_reconcile: invalid algo_id, skipping")
		return false
	}
	l := ar.log.With().Int64("trade_id", tradeID).Str("symbol", symbol).Int64("algo_id", algoID).Logger()
	q, err := ar.bc.QueryAlgoOrder(ctx, algoID)
	if err != nil {
		l.Warn().Err(err).Msg("algo.try_reconcile: query failed")
		return false
	}
	if q.AlgoStatus != "FINISHED" {
		return false
	}
	return ar.autoCloseFromFields(ctx, tradeID, symbol, q, entryPrice, currentQty, entryTs, l)
}

// autoClose adapts the original row-based call site to the new primitive
// helper. Used by ReconcileTick (proactive sweep).
func (ar *AlgoReconciler) autoClose(ctx context.Context, r gen.ListOpenTradesWithAlgoRow, q binance.AlgoOrderQuery, log zerolog.Logger) bool {
	entryTs := time.Time{}
	if r.EntryTs.Valid {
		entryTs = r.EntryTs.Time
	}
	return ar.autoCloseFromFields(ctx, r.ID, r.Symbol, q,
		decimalFromPgNumeric(r.EntryPrice),
		decimalFromPgNumeric(r.CurrentQty),
		entryTs, log)
}

// autoCloseFromFields is the shared close pipeline taking primitive inputs.
// Mirrors exit_manager.persistClose so the close artifacts look identical to
// a soft/hard timeout exit, just with exit_reason='disaster' and price from
// Algo.actualPrice.
func (ar *AlgoReconciler) autoCloseFromFields(ctx context.Context, tradeID int64, symbol string, q binance.AlgoOrderQuery, entryPrice, currentQty decimal.Decimal, entryTs time.Time, log zerolog.Logger) bool {
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
		Type:    ExitReasonDisaster,
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
		ExitReason:  pgtype.Text{String: ExitReasonDisaster, Valid: true},
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
	metrics.AlgoTriggeredTotal.WithLabelValues(symbol, ExitReasonDisaster).Inc()

	holdHours := 0.0
	if !entryTs.IsZero() {
		holdHours = now.Sub(entryTs).Hours()
	}
	metrics.PositionHoldDurationHours.WithLabelValues(ExitReasonDisaster).Observe(holdHours)

	log.Info().
		Str("close_price", closePrice.String()).
		Str("realized_pnl", realizedPnl.String()).
		Str("qty", qty.String()).
		Float64("hold_hours", holdHours).
		Time("trigger_time", q.TriggerTime).
		Msg("algo.triggered.auto_close")
	return true
}
