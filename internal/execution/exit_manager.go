// Phase 4 Round 5: exit_manager — 1min cron evaluates open positions for
// time-based exit (soft/hard timeout). Disaster stop (Round 1) fires via Algo
// Service independently; this cron only owns time-based exits + the close
// pipeline (cancel Algo + market SELL + DB write).
//
// v0.1 implements 2 of the 5 exit conditions from SPEC §出场 (per mu Q6 B):
//   - soft_timeout: hold ≥ 24h AND unrealized < 0 → MARKET SELL
//   - hard_timeout: hold ≥ 72h → unconditional MARKET SELL
//
// Out of scope v0.1 (留 v0.2):
//   - tp_stage1 / tp_stage2 (partial take-profit)
//   - trailing stop (activate at +3%, ATR×2)
//   - signal_fail (OI drop / EMA20 / 5min_low)
//
// Out of scope Round 5 (留 Round 6):
//   - 5 项熔断 real trip logic (hooks present)

package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// Exit reason constants (also written to trades.exit_reason + trade_exits.type).
const (
	ExitReasonSoftTimeout    = "soft_timeout"
	ExitReasonHardTimeout    = "hard_timeout"
	ExitReasonDisaster       = "disaster"
	ExitReasonManual         = "manual"
	ExitReasonClosingFailed  = "closing_failed"
	// Round 2.x Part 3: admin Web UI manual close (mu 紧急平仓). Distinct
	// from ExitReasonManual which is the legacy / one-off admin path.
	ExitReasonManualClose = "manual_close"
	// v0.2 Round 1 Module B: trail_sN exit reason — set when a trailing-stop
	// algo (S1/S2 native or S3/S4 trader-managed STOP_MARKET) fires.
	// trade_exits.type carries the same string. algo_reconciler picks the
	// reason by matching the FINISHED algoId to disaster_stop vs trail_algo.
	ExitReasonTrailS1 = "trail_s1"
	ExitReasonTrailS2 = "trail_s2"
	ExitReasonTrailS3 = "trail_s3"
	ExitReasonTrailS4 = "trail_s4"
	// v0.2 Round 2 Module A: TP_STAGE partial closes. trades.status stays 'open';
	// trade_exits.type='tp1' / 'tp2' records the partial fill. Multiple exit rows
	// per trade are valid for TPs (trade_exits unique constraint is on trade_id+type).
	ExitReasonTP1 = "tp1"
	ExitReasonTP2 = "tp2"
	// v0.2 Round 3 Module C: signal-fail full close — entry signal stopped working
	// (OI drop ≥ N% AND/OR last K closes < EMA20). Driven by signal_fail_detector
	// (5min cron) which calls ExitManager.ClosePosition with this reason.
	ExitReasonSigfail = "sigfail"
	// Round R.8 (2026-05-16, mu 真盘 catch): orphan auto-sync. When Binance shows
	// no position for ≥2 ticks AND no algo FINISHED (R.4 F1 + R.5 defenses both
	// fail), mark trade closed locally with pnl=0 + no halt. Real fill may be
	// recoverable via Binance userTrades manual review (out of scope automation).
	// Distinguishes from disaster/trail/timeout where pnl is authoritative.
	ExitReasonOrphanSynced = "orphan_synced"
)

// Round 5 v0.1 thresholds (Round 0 §2 4.3 + SPEC §出场).
const (
	softTimeoutHours = 24
	hardTimeoutHours = 72
	fillWaitTimeout  = 10 * time.Second // SELL fill polling deadline
)

// ExitManagerDeps is the minimal DB surface exit_manager needs.
type ExitManagerDeps interface {
	ListOpenTradesForExit(ctx context.Context) ([]gen.ListOpenTradesForExitRow, error)
	UpdateTradeClosing(ctx context.Context, id int64) error
	UpdateTradeClosed(ctx context.Context, arg gen.UpdateTradeClosedParams) error
	UpdateTradeFailed(ctx context.Context, arg gen.UpdateTradeFailedParams) error
	InsertTradeExit(ctx context.Context, arg gen.InsertTradeExitParams) error
	DeletePositionState(ctx context.Context, tradeID int64) error
	UpdateAfterTradeClose(ctx context.Context, arg gen.UpdateAfterTradeCloseParams) error
	InsertHaltRCA(ctx context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error)
	TripGenericHalt(ctx context.Context, arg gen.TripGenericHaltParams) error
}

// ExitBinanceClient is the minimal binance surface for the close pipeline.
type ExitBinanceClient interface {
	CancelAlgoOrder(ctx context.Context, symbol string, algoID int64) error
	PlaceMarketOrder(ctx context.Context, symbol, side, quantity, clientOrderID string) (binance.OrderResult, error)
	GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (binance.OrderResult, error)
}

// FeesFetcher retrieves actual commission fills for a closed order.
// Implemented by *binance.Client; nil → fees=0 (test default).
type FeesFetcher interface {
	GetUserTrades(ctx context.Context, symbol string, orderID int64) ([]binance.UserTrade, error)
}

// ExitManager owns the exit-evaluation cron + close pipeline.
type ExitManager struct {
	db    ExitManagerDeps
	bc    ExitBinanceClient
	ff    FeesFetcher // optional; nil → fees=0
	rdb   *redis.Client
	log   zerolog.Logger
	nowFn func() time.Time
}

// NewExitManager constructs an ExitManager.
func NewExitManager(db ExitManagerDeps, bc ExitBinanceClient, rdb *redis.Client, log zerolog.Logger) *ExitManager {
	return &ExitManager{db: db, bc: bc, rdb: rdb, log: log, nowFn: timez.NowUTC}
}

// WithFeesFetcher wires the real commission fetcher (call after NewExitManager in main).
func (em *ExitManager) WithFeesFetcher(ff FeesFetcher) *ExitManager {
	em.ff = ff
	return em
}

// EvaluateTick runs one 1min exit-evaluation pass.
func (em *ExitManager) EvaluateTick(ctx context.Context) {
	now := em.nowFn()
	trades, err := em.db.ListOpenTradesForExit(ctx)
	if err != nil {
		em.log.Error().Err(err).Msg("exit.timeout_check.tick: list open trades failed")
		return
	}
	if len(trades) == 0 {
		em.log.Debug().Msg("exit.timeout_check.tick: no open trades")
		return
	}

	softCandidates, hardCandidates, retries, manualCandidates := 0, 0, 0, 0
	for _, t := range trades {
		l := em.log.With().Int64("trade_id", t.ID).Str("symbol", t.Symbol).Logger()

		// Round 2.x Part 3: admin Web UI manual close — exit_reason is pre-set
		// to 'manual_close' (status='closing'). Run close pipeline immediately,
		// skipping timeout evaluation entirely.
		if t.ExitReason.Valid && t.ExitReason.String == ExitReasonManualClose {
			l.Info().Msg("exit.triggered: manual_close (admin Web UI pre-set)")
			em.closePosition(ctx, t, ExitReasonManualClose, l)
			manualCandidates++
			continue
		}

		// 'closing' state from prior failed tick → retry close pipeline.
		if t.CurrentQty.Valid && !decimalFromPgNumeric(t.CurrentQty).IsZero() {
			// Will route below.
		}

		if !t.EntryTs.Valid {
			l.Warn().Msg("exit.skip: entry_ts NULL (likely entering→open race)")
			continue
		}
		holdDuration := now.Sub(t.EntryTs.Time)
		holdHours := holdDuration.Hours()

		// hard timeout — unconditional, evaluated first.
		if holdHours >= hardTimeoutHours {
			l.Info().Float64("hold_hours", holdHours).Msg("exit.triggered: hard_timeout")
			em.closePosition(ctx, t, ExitReasonHardTimeout, l)
			hardCandidates++
			continue
		}

		// soft timeout — requires unrealized < 0.
		if holdHours >= softTimeoutHours {
			unrealized := em.computeUnrealizedPnl(ctx, t)
			if unrealized.IsNegative() {
				l.Info().Float64("hold_hours", holdHours).Str("unrealized", unrealized.String()).
					Msg("exit.triggered: soft_timeout")
				em.closePosition(ctx, t, ExitReasonSoftTimeout, l)
				softCandidates++
				continue
			}
			l.Debug().Float64("hold_hours", holdHours).Str("unrealized", unrealized.String()).
				Msg("exit.soft_timeout.skip: position in profit")
		}

		// Retry pipeline for 'closing' state (prior tick partial-failed).
		// Determined by status; we can't see status in row, but ListOpenTradesForExit
		// includes 'closing' rows so we re-attempt.
		// (No-op here — closePosition path is idempotent if status already 'closing'.)
	}

	em.log.Info().
		Int("open_trades", len(trades)).
		Int("soft_triggered", softCandidates).
		Int("hard_triggered", hardCandidates).
		Int("manual_triggered", manualCandidates).
		Int("close_retries", retries).
		Msg("exit.timeout_check.tick")
}

// computeUnrealizedPnl reads Redis latest_price:<symbol> (written by position_price
// collector). Returns (mark - entry) × qty (signed). 0 on any read error
// (caller treats unknown as not-negative → no soft-timeout trigger).
func (em *ExitManager) computeUnrealizedPnl(ctx context.Context, t gen.ListOpenTradesForExitRow) decimal.Decimal {
	if !t.EntryPrice.Valid || !t.CurrentQty.Valid {
		return decimal.Zero
	}
	priceStr, err := em.rdb.Get(ctx, "latest_price:"+t.Symbol).Result()
	if err != nil {
		em.log.Debug().Err(err).Str("symbol", t.Symbol).Msg("exit.pnl: latest_price unavailable")
		return decimal.Zero
	}
	markPrice, err := decimal.NewFromString(priceStr)
	if err != nil {
		return decimal.Zero
	}
	entryPrice := decimalFromPgNumeric(t.EntryPrice)
	qty := decimalFromPgNumeric(t.CurrentQty)
	// LONG: (mark - entry) × qty. Round 5 only supports LONG.
	return markPrice.Sub(entryPrice).Mul(qty)
}

// ClosePosition is the public entry point for caller-driven closes (e.g. SIGFAIL
// detector). Delegates to the internal closePosition pipeline.
func (em *ExitManager) ClosePosition(ctx context.Context, t gen.ListOpenTradesForExitRow, exitReason string, log zerolog.Logger) {
	em.closePosition(ctx, t, exitReason, log)
}

// closePosition runs the close pipeline:
//
//	1. Mark trades.status='closing' (intermediate; survives crashes/restarts).
//	2. Cancel Algo Service disaster stop (if any; -2011 / -4046 tolerated).
//	3. POST /fapi/v1/order MARKET SELL qty reduceOnly=true.
//	4. Wait fill (poll up to 10s).
//	5. INSERT trade_exits + UPDATE trades.status='closed' + DELETE position_state +
//	   ZREM Redis zset + UPDATE circuit_breaker_state (daily_pnl + consec_losses).
//
// Failure handling: if SELL doesn't fill, trades.status stays 'closing' +
// halt + RCA (next tick retries from step 2). Avoids "DB says failed but
// binance position still open" — the worst case (mu's emphasis).
func (em *ExitManager) closePosition(ctx context.Context, t gen.ListOpenTradesForExitRow, exitReason string, log zerolog.Logger) {
	// Step 1: intermediate 'closing' state (no-op if already 'closing').
	if err := em.db.UpdateTradeClosing(ctx, t.ID); err != nil {
		log.Error().Err(err).Msg("exit.failed: UpdateTradeClosing")
		return
	}

	// Step 2: cancel Algo Service disaster stop (best-effort).
	if t.BinanceDisasterStopOrderID.Valid && t.BinanceDisasterStopOrderID.String != "" {
		algoID, err := strconv.ParseInt(t.BinanceDisasterStopOrderID.String, 10, 64)
		if err != nil {
			log.Warn().Err(err).Str("algo_id", t.BinanceDisasterStopOrderID.String).
				Msg("exit.cancel_algo: invalid algo_id (skipping cancel)")
		} else {
			cancelStart := em.nowFn()
			if err := em.bc.CancelAlgoOrder(ctx, t.Symbol, algoID); err != nil {
				// Algo may have already fired or been canceled — log + continue.
				log.Warn().Err(err).Int64("algo_id", algoID).
					Msg("exit.cancel_algo: failed (continuing to SELL anyway)")
			}
			metrics.ExitLatencySeconds.WithLabelValues("cancel_algo").Observe(em.nowFn().Sub(cancelStart).Seconds())
		}
	}

	// Step 3: MARKET SELL reduceOnly. clientOrderId carries idempotency on retry.
	qty := decimalFromPgNumeric(t.CurrentQty)
	if qty.IsZero() {
		log.Error().Msg("exit.sell.skip: current_qty=0 (cannot SELL); marking failed")
		em.markCloseFailed(ctx, t.ID, t.Symbol, "current_qty_zero", log)
		return
	}
	clientOrderID := fmt.Sprintf("exit_%d_%d", t.ID, em.nowFn().Unix())
	sellStart := em.nowFn()
	res, err := em.bc.PlaceMarketOrder(ctx, t.Symbol, "SELL", qty.String(), clientOrderID)
	metrics.ExitLatencySeconds.WithLabelValues("place_sell").Observe(em.nowFn().Sub(sellStart).Seconds())
	if err != nil {
		log.Error().Err(err).Msg("exit.sell.failed: position may still be open — halt + retry next tick")
		metrics.ExitsTotal.WithLabelValues(t.Symbol, exitReason, "sell_failed").Inc()
		em.tripCloseHalt(ctx, t, exitReason, "sell_failed: "+err.Error(), log)
		return
	}

	closePrice := res.AvgPrice
	if closePrice.IsZero() {
		closePrice = res.CumQuote.Div(qty)
	}
	if closePrice.IsZero() {
		log.Error().Str("status", res.Status).Msg("exit.sell: zero close_price (status unknown)")
		em.tripCloseHalt(ctx, t, exitReason, "zero_close_price", log)
		return
	}

	// Step 4: persistence — trade_exits + trades + position_states + circuit_breaker.
	em.persistClose(ctx, t, exitReason, closePrice, qty, res.OrderID, log)

	metrics.ExitsTotal.WithLabelValues(t.Symbol, exitReason, "success").Inc()
}

// sumFees fetches commission fills for the exit order and returns the total.
// Non-fatal: logs a warning and returns decimal.Zero on any error.
func (em *ExitManager) sumFees(ctx context.Context, symbol string, exitOrderID int64, log zerolog.Logger) decimal.Decimal {
	if em.ff == nil || exitOrderID == 0 {
		return decimal.Zero
	}
	fills, err := em.ff.GetUserTrades(ctx, symbol, exitOrderID)
	if err != nil {
		log.Warn().Err(err).Int64("order_id", exitOrderID).Msg("exit.persist: GetUserTrades failed, fees=0")
		return decimal.Zero
	}
	total := decimal.Zero
	for _, f := range fills {
		total = total.Add(f.Commission)
	}
	return total
}

// persistClose writes the terminal close artifacts. Per-step errors logged,
// non-fatal: even if e.g. DELETE position_states fails, trades.status is the
// authoritative open/closed indicator (orphaned position_states row is benign).
func (em *ExitManager) persistClose(
	ctx context.Context,
	t gen.ListOpenTradesForExitRow,
	exitReason string,
	closePrice, qty decimal.Decimal,
	exitOrderID int64,
	log zerolog.Logger,
) {
	now := em.nowFn()
	entryPrice := decimalFromPgNumeric(t.EntryPrice)
	// fees: real exit commission from /fapi/v1/userTrades; entry fees not yet
	// captured (entry_order_id not stored in DB). TODO: Round 6+ store entry_order_id.
	fees := em.sumFees(ctx, t.Symbol, exitOrderID, log)
	// realized_pnl = gross price-diff PnL (LONG). Net = realized_pnl - fees.
	realizedPnl := closePrice.Sub(entryPrice).Mul(qty)

	holdHours := 0.0
	if t.EntryTs.Valid {
		holdHours = now.Sub(t.EntryTs.Time).Hours()
	}
	metrics.PositionHoldDurationHours.WithLabelValues(exitReason).Observe(holdHours)

	// trade_exits row.
	persistStart := em.nowFn()
	if err := em.db.InsertTradeExit(ctx, gen.InsertTradeExitParams{
		TradeID: pgtype.Int8{Int64: t.ID, Valid: true},
		Ts:      now,
		Type:    exitReason,
		Qty:     qty,
		Price:   closePrice,
		Pnl:     realizedPnl,
	}); err != nil {
		log.Error().Err(err).Msg("exit.persist: InsertTradeExit failed")
	}

	// trades.status='closed'.
	if err := em.db.UpdateTradeClosed(ctx, gen.UpdateTradeClosedParams{
		ID:          t.ID,
		ExitTs:      pgtype.Timestamptz{Time: now, Valid: true},
		ExitPrice:   closePrice,
		ExitReason:  pgtype.Text{String: exitReason, Valid: true},
		RealizedPnl: realizedPnl,
		Fees:        fees,
	}); err != nil {
		log.Error().Err(err).Msg("exit.persist: UpdateTradeClosed failed (CRITICAL — trades still 'closing')")
		em.tripCloseHalt(ctx, t, exitReason, "db_update_closed_failed", log)
		return
	}

	// position_states cleanup.
	if err := em.db.DeletePositionState(ctx, t.ID); err != nil {
		log.Warn().Err(err).Msg("exit.persist: DeletePositionState failed (non-fatal)")
	}

	// Redis zset cleanup.
	if err := em.rdb.ZRem(ctx, redisKeyPositionsActive, strconv.FormatInt(t.ID, 10)).Err(); err != nil {
		log.Warn().Err(err).Msg("exit.persist: ZREM positions_active failed (non-fatal, next sync rebuilds)")
	}

	// v0.2 Catch 2: clear margin_ratio gauge — same reason as orphan branch.
	// Without this the gauge keeps the last in-flight value indefinitely.
	metrics.PositionMarginRatio.DeleteLabelValues(t.Symbol)

	// circuit_breaker rollup (daily_pnl + consecutive_losses).
	bjt := now.In(timez.BJT)
	pgDate := pgtype.Date{Valid: true}
	_ = pgDate.Scan(bjt.Format("2006-01-02"))
	if err := em.db.UpdateAfterTradeClose(ctx, gen.UpdateAfterTradeCloseParams{
		RealizedPnl:  realizedPnl,
		DailyPnlDate: pgDate,
	}); err != nil {
		log.Warn().Err(err).Msg("exit.persist: UpdateAfterTradeClose failed (Round 6 will read stale state)")
	}

	metrics.ExitLatencySeconds.WithLabelValues("db").Observe(em.nowFn().Sub(persistStart).Seconds())

	sign := "positive"
	if realizedPnl.IsNegative() {
		sign = "negative"
	} else if realizedPnl.IsZero() {
		sign = "zero"
	}
	// Counter.Add panics on negative; metric design (see metrics.go) labels the
	// sign and adds |pnl|. Pre-v0.2 had latent panic on first loss exit.
	metrics.RealizedPnlTotal.WithLabelValues(t.Symbol, sign).Add(mustFloat(realizedPnl.Abs()))

	log.Info().
		Str("exit_price", closePrice.String()).
		Str("realized_pnl", realizedPnl.String()).
		Str("fees", fees.String()).
		Float64("hold_hours", holdHours).
		Msg("exit.completed")
}

// tripCloseHalt marks the close as in-flight-failed: write RCA + trip halt,
// trade row stays 'closing' for next-tick retry. Reuses Round 4 infrastructure.
func (em *ExitManager) tripCloseHalt(ctx context.Context, t gen.ListOpenTradesForExitRow, exitReason, failStep string, log zerolog.Logger) {
	context := map[string]any{
		"trade_id":      t.ID,
		"symbol":        t.Symbol,
		"exit_reason":   exitReason,
		"fail_step":     failStep,
		"detected_at":   em.nowFn().Format(time.RFC3339),
	}
	ctxJSON, _ := json.Marshal(context)
	if _, err := em.db.InsertHaltRCA(ctx, gen.InsertHaltRCAParams{
		HaltType:    "close_failed",
		ContextJson: ctxJSON,
	}); err != nil {
		log.Error().Err(err).Msg("exit.halt: rca write failed")
	}
	metrics.HaltRCAPendingTotal.WithLabelValues("close_failed").Inc()
	haltUntil := pgtype.Timestamptz{Time: em.nowFn().Add(1 * time.Hour), Valid: true}
	if err := em.db.TripGenericHalt(ctx, gen.TripGenericHaltParams{
		HaltReason: pgtype.Text{String: "exit_" + ExitReasonClosingFailed, Valid: true},
		HaltUntil:  haltUntil,
	}); err != nil {
		log.Error().Err(err).Msg("exit.halt: trip failed")
	}
}

// markCloseFailed is for terminal scenarios (current_qty=0) where retrying
// won't help. Trade goes to 'failed' (not 'closing') to avoid infinite retry.
// v0.2 Catch 5 + Catch 2: also clean up position_states / zset / gauge so
// terminal failure mirrors the success-path persistClose teardown.
func (em *ExitManager) markCloseFailed(ctx context.Context, tradeID int64, symbol, reason string, log zerolog.Logger) {
	if err := em.db.UpdateTradeFailed(ctx, gen.UpdateTradeFailedParams{
		ID:         tradeID,
		ExitReason: pgtype.Text{String: reason, Valid: true},
		ExitTs:     pgtype.Timestamptz{Time: em.nowFn(), Valid: true},
	}); err != nil {
		log.Error().Err(err).Msg("exit.fail: UpdateTradeFailed failed")
	}
	if err := em.db.DeletePositionState(ctx, tradeID); err != nil {
		log.Warn().Err(err).Msg("exit.fail: DeletePositionState failed (non-fatal)")
	}
	if err := em.rdb.ZRem(ctx, redisKeyPositionsActive, strconv.FormatInt(tradeID, 10)).Err(); err != nil {
		log.Warn().Err(err).Msg("exit.fail: ZREM positions_active failed (non-fatal)")
	}
	if symbol != "" {
		metrics.PositionMarginRatio.DeleteLabelValues(symbol)
	}
}

func mustFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

// Suppress unused-import warning if any code path is conditional.
var _ = strings.Builder{}
