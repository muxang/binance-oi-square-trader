// Package execution places entry orders, attaches disaster stop orders, and triggers exits.
//
// Phase 4 Round 1: PlaceEntry implements Step 1-9 of the complete entry flow.
// The executor is called from the decision_engine collector in a goroutine;
// all errors are handled internally (log + DB mark-failed) and never bubble up.
//
// ref: PHASE_4_DESIGN.md §2 Step 1-9
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
package execution

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// Config holds execution-layer constants sourced from environment config.
type Config struct {
	// DisasterStopPct is the fraction below entry price for the Algo stop trigger.
	// Env: DISASTER_STOP_PCT (e.g. 0.06 → stop at entry × 0.94).
	DisasterStopPct decimal.Decimal
	// Leverage is the target initial leverage for all new positions.
	// Env: LEVERAGE (e.g. 10).
	Leverage int
}

// Executor executes the complete trade entry lifecycle (Step 1-9).
// All public methods are safe for concurrent use.
type Executor struct {
	bc    *binance.Client
	db    *gen.Queries
	cfg   Config
	log   zerolog.Logger
	nowFn func() time.Time
}

// New creates an Executor. bc and db must not be nil.
func New(bc *binance.Client, db *gen.Queries, cfg Config, log zerolog.Logger) *Executor {
	return &Executor{bc: bc, db: db, cfg: cfg, log: log, nowFn: timez.NowUTC}
}

// PlaceEntry runs Steps 1-9 for a trade that passed the decision engine.
// tradeID is the trades.id already inserted (status='entering') by RunTick.
// All failures are handled internally: DB is marked failed, error is logged,
// and the function returns without propagating — callers use fire-and-forget.
//
// Internal deadline: 60 seconds for the full Steps 1-9 sequence.
func (e *Executor) PlaceEntry(
	ctx context.Context,
	tradeID, signalID int64,
	symbol, decision string,
	qty, margin, notional, entryPriceEst, tickSize decimal.Decimal,
	leverage int32,
) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			e.log.Error().
				Interface("panic", r).
				Int64("trade_id", tradeID).
				Str("symbol", symbol).
				Msg("order.panic: recovered in PlaceEntry")
			metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		}
	}()

	log := e.log.With().
		Int64("trade_id", tradeID).
		Int64("signal_id", signalID).
		Str("symbol", symbol).
		Str("decision", decision).
		Logger()

	clientOrderID := fmt.Sprintf("t%d_r0", signalID)

	log.Info().
		Str("qty", qty.String()).
		Str("margin", margin.String()).
		Str("notional", notional.String()).
		Int32("leverage", leverage).
		Msg("order.entering")

	// Step 1: setMarginType ISOLATED (idempotent; -4046 is silently accepted).
	start := e.nowFn()
	if err := e.bc.SetMarginType(ctx, symbol, "ISOLATED"); err != nil {
		metrics.OrderLatencySeconds.WithLabelValues("margin").Observe(e.nowFn().Sub(start).Seconds())
		log.Error().Err(err).Msg("order.failed: set_margin_type")
		e.markFailed(ctx, tradeID, "set_margin_type_failed")
		metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		return
	}
	metrics.OrderLatencySeconds.WithLabelValues("margin").Observe(e.nowFn().Sub(start).Seconds())

	// Step 2: setLeverage (idempotent; -4059 is silently accepted).
	start = e.nowFn()
	if _, err := e.bc.SetLeverage(ctx, symbol, e.cfg.Leverage); err != nil {
		metrics.OrderLatencySeconds.WithLabelValues("leverage").Observe(e.nowFn().Sub(start).Seconds())
		log.Error().Err(err).Msg("order.failed: set_leverage")
		e.markFailed(ctx, tradeID, "set_leverage_failed")
		metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		return
	}
	metrics.OrderLatencySeconds.WithLabelValues("leverage").Observe(e.nowFn().Sub(start).Seconds())

	// Step 4: place MARKET BUY order. client_order_id set at INSERT time by RunTick.
	start = e.nowFn()
	orderResult, err := e.bc.PlaceMarketOrder(ctx, symbol, "BUY", qty.String(), clientOrderID)
	metrics.OrderLatencySeconds.WithLabelValues("place").Observe(e.nowFn().Sub(start).Seconds())
	if err != nil {
		log.Error().Err(err).Msg("order.failed: place_market_order")
		e.markFailed(ctx, tradeID, "place_order_failed")
		metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		return
	}

	// Step 5: wait fill. RESULT mode market orders are typically FILLED immediately.
	fillStart := e.nowFn()
	start = e.nowFn()
	filled, err := e.waitFill(ctx, symbol, orderResult, 10*time.Second)
	metrics.OrderLatencySeconds.WithLabelValues("fill").Observe(e.nowFn().Sub(start).Seconds())
	if err != nil {
		log.Error().Err(err).Int64("order_id", orderResult.OrderID).Msg("order.failed: fill_timeout")
		_ = e.bc.CancelOrder(ctx, symbol, orderResult.OrderID)
		e.markFailed(ctx, tradeID, "fill_timeout")
		metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		return
	}

	fillLatencyMs := e.nowFn().Sub(fillStart).Milliseconds()
	log.Info().
		Str("entry_price", filled.AvgPrice.String()).
		Str("qty", filled.ExecutedQty.String()).
		Int64("fill_latency_ms", fillLatencyMs).
		Msg("order.filled")

	// UPDATE trades status='open', entry_ts, entry_price.
	now := e.nowFn()
	if err := e.db.UpdateTradeOpen(ctx, gen.UpdateTradeOpenParams{
		ID:         tradeID,
		EntryTs:    now,
		EntryPrice: filled.AvgPrice,
	}); err != nil {
		// DB write failed post-fill: position is OPEN on exchange but DB says 'entering'.
		// Emergency close to avoid orphaned position without disaster stop.
		log.Error().Err(err).Str("avg_price", filled.AvgPrice.String()).
			Msg("order.failed: update_trade_open — emergency closing")
		e.emergencyClose(ctx, tradeID, symbol, filled.ExecutedQty, filled.AvgPrice,
			"db_update_open_failed", log)
		metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "failed").Inc()
		return
	}

	metrics.OrdersTotal.WithLabelValues(symbol, "BUY", decision, "success").Inc()

	// Steps 6-9: place Algo Service disaster stop.
	e.placeDisasterStop(ctx, tradeID, symbol, filled.ExecutedQty, filled.AvgPrice, tickSize, log)
}

// waitFill polls until the order is FILLED or the deadline is exceeded.
// RESULT mode returns FILLED immediately for market orders in the vast majority
// of cases; the poll path handles the rare partial-fill edge case.
func (e *Executor) waitFill(ctx context.Context, symbol string, initial binance.OrderResult, timeout time.Duration) (binance.OrderResult, error) {
	if initial.Status == "FILLED" {
		return initial, nil
	}
	deadline := e.nowFn().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return binance.OrderResult{}, ctx.Err()
		case <-ticker.C:
			if e.nowFn().After(deadline) {
				return binance.OrderResult{}, fmt.Errorf("order %d not FILLED after %s", initial.OrderID, timeout)
			}
			r, err := e.bc.GetOrder(ctx, symbol, initial.OrderID)
			if err != nil {
				continue // transient error — keep polling until deadline
			}
			if r.Status == "FILLED" {
				return r, nil
			}
		}
	}
}

// placeDisasterStop implements Steps 6-9: Algo Service STOP_MARKET at
// entry × (1 - DisasterStopPct), then position_states INSERT.
// Step 9 failure triggers emergencyClose + circuit_breaker trip.
func (e *Executor) placeDisasterStop(
	ctx context.Context,
	tradeID int64,
	symbol string,
	fillQty, fillPrice, tickSize decimal.Decimal,
	log zerolog.Logger,
) {
	one := decimal.NewFromInt(1)
	stopPrice := fillPrice.Mul(one.Sub(e.cfg.DisasterStopPct))
	// Round to symbol tickSize multiple — Binance rejects with -1111 otherwise.
	// Truncate (floor for positive) = round down = looser stop, conservative re slippage.
	if !tickSize.IsZero() {
		stopPrice = stopPrice.Div(tickSize).Truncate(0).Mul(tickSize)
	}

	start := e.nowFn()
	algoResult, err := e.bc.PlaceAlgoConditionalStop(ctx, symbol, fillQty.String(), stopPrice.String())
	metrics.OrderLatencySeconds.WithLabelValues("algo").Observe(e.nowFn().Sub(start).Seconds())

	if err != nil {
		// Step 9 failure fallback: emergency market close to avoid naked long.
		log.Error().Err(err).
			Str("stop_price", stopPrice.String()).
			Msg("order.disaster_stop.failed: initiating emergency close")
		metrics.DisasterStopsPlacedTotal.WithLabelValues(symbol, "failed").Inc()
		e.emergencyClose(ctx, tradeID, symbol, fillQty, fillPrice,
			"disaster_stop_placement_failed", log)
		// Trip circuit breaker (per Round 0 mu decision: log-only halt, no halt_until).
		if cbErr := e.db.TripDisasterStopFailHalt(ctx); cbErr != nil {
			log.Error().Err(cbErr).Msg("order.disaster_stop.failed: circuit_breaker trip also failed")
		}
		return
	}

	algoIDStr := strconv.FormatInt(algoResult.AlgoID, 10)
	log.Info().
		Str("algo_id", algoIDStr).
		Str("stop_price", stopPrice.String()).
		Str("status", algoResult.Status).
		Msg("order.disaster_stop.placed")
	metrics.DisasterStopsPlacedTotal.WithLabelValues(symbol, "success").Inc()

	// Step 7: persist disaster stop order ID.
	if err := e.db.UpdateTradeDisasterStop(ctx, gen.UpdateTradeDisasterStopParams{
		ID:                         tradeID,
		BinanceDisasterStopOrderID: pgtype.Text{String: algoIDStr, Valid: true},
	}); err != nil {
		log.Error().Err(err).Str("algo_id", algoIDStr).
			Msg("order.disaster_stop.placed: update trade failed (non-fatal, disaster stop IS active)")
	}

	// Step 8: INSERT position_states.
	now := e.nowFn()
	if err := e.db.InsertPositionState(ctx, gen.InsertPositionStateParams{
		TradeID:      tradeID,
		CurrentQty:   fillQty,
		HighestPrice: fillPrice,
		LastCheckTs:  now,
	}); err != nil {
		log.Error().Err(err).Msg("order.disaster_stop.placed: insert position_state failed (non-fatal)")
	}
}

// emergencyClose places an immediate MARKET SELL to close the position when the
// disaster stop cannot be placed. Records an emergency_close trade_exits row and
// marks the trade failed.
func (e *Executor) emergencyClose(
	ctx context.Context,
	tradeID int64,
	symbol string,
	qty, approxPrice decimal.Decimal,
	reason string,
	log zerolog.Logger,
) {
	closeResult, err := e.bc.PlaceMarketOrder(ctx, symbol, "SELL", qty.String(), "")
	now := e.nowFn()

	actualPrice := approxPrice
	pnl := decimal.Zero
	if err != nil {
		log.Error().Err(err).Str("symbol", symbol).Str("qty", qty.String()).
			Msg("order.emergency_close.FAILED: position may be orphaned — operator intervention required")
	} else {
		actualPrice = closeResult.AvgPrice
		log.Info().Str("close_price", actualPrice.String()).Msg("order.emergency_close: filled")
	}

	// Record exit event regardless of close success (best-effort).
	_ = e.db.InsertTradeExit(ctx, gen.InsertTradeExitParams{
		TradeID: pgtype.Int8{Int64: tradeID, Valid: true},
		Ts:      now,
		Type:    "emergency_close",
		Qty:     qty,
		Price:   actualPrice,
		Pnl:     pnl,
	})

	e.markFailed(ctx, tradeID, reason)
}

// markFailed sets trades.status='failed' with exit_reason and exit_ts=now.
func (e *Executor) markFailed(ctx context.Context, tradeID int64, reason string) {
	now := e.nowFn()
	if err := e.db.UpdateTradeFailed(ctx, gen.UpdateTradeFailedParams{
		ID:         tradeID,
		ExitReason: pgtype.Text{String: reason, Valid: true},
		ExitTs:     pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		e.log.Error().Err(err).Int64("trade_id", tradeID).Str("reason", reason).
			Msg("order.mark_failed: DB update failed (trade may remain stuck in 'entering')")
	}
}
