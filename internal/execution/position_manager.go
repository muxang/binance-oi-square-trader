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
)

// PositionSyncDeps is the minimal DB surface position_manager needs.
type PositionSyncDeps interface {
	ListOpenTradesForSync(ctx context.Context) ([]gen.ListOpenTradesForSyncRow, error)
	UpdatePositionStateSync(ctx context.Context, arg gen.UpdatePositionStateSyncParams) error
	UpdateTradeFailed(ctx context.Context, arg gen.UpdateTradeFailedParams) error
	InsertTradeExit(ctx context.Context, arg gen.InsertTradeExitParams) error
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
}

// NewPositionManager constructs a PositionManager.
func NewPositionManager(db PositionSyncDeps, bc PositionRiskFetcher, rdb *redis.Client, log zerolog.Logger) *PositionManager {
	return &PositionManager{db: db, bc: bc, rdb: rdb, log: log, nowFn: timez.NowUTC}
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

	syncedOK, drift, marginCalls := 0, 0, 0
	for _, t := range trades {
		l := pm.log.With().Int64("trade_id", t.ID).Str("symbol", t.Symbol).Logger()

		pos, ok := positionsBySymbol[t.Symbol]
		if !ok {
			// Binance has no position for this symbol — drift type=missing.
			l.Warn().Msg("position.sync.drift: open trade has no Binance position (missing)")
			metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "missing").Inc()
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
			drift++
		}

		// Qty drift: compare abs(positionAmt) to DB current_qty (or notional/entry if state empty).
		absAmt := pos.PositionAmt.Abs()
		if t.CurrentQty.Valid {
			dbQty := decimalFromPgNumeric(t.CurrentQty)
			if !dbQty.IsZero() && dbQty.Sub(absAmt).Abs().Div(dbQty).GreaterThan(decimal.NewFromFloat(0.01)) {
				l.Warn().Str("db_qty", dbQty.String()).Str("binance_qty", absAmt.String()).
					Msg("position.sync.drift: qty mismatch > 1%")
				metrics.PositionSyncDriftTotal.WithLabelValues(t.Symbol, "qty").Inc()
				drift++
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

	pm.rebuildRedisZset(ctx, trades)

	pm.log.Info().
		Int("open_trades", len(trades)).
		Int("synced_ok", syncedOK).
		Int("drift", drift).
		Int("margin_calls", marginCalls).
		Msg("position.sync.tick")

	if drift > 0 {
		metrics.PositionSyncRunsTotal.WithLabelValues("drift").Inc()
	} else {
		metrics.PositionSyncRunsTotal.WithLabelValues("ok").Inc()
	}
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
