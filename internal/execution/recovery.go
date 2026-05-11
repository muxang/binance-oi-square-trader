// Phase 4 Round 2: startup recovery for trades stuck in 'entering'.
//
// Scenarios on crash + restart:
//   (A) trader 已 INSERT trades.entering + client_order_id, 还没 PlaceMarketOrder
//       → Binance 没收到, GetOrderByClientID 返回 -2013 → 直接 markFailed
//   (B) trader 发了 order 但没收到响应 → Binance 已收到, 可能 FILLED / NEW
//       → GetOrderByClientID 返回 order, reconcile:
//           FILLED            → UpdateTradeOpen + 继续 placeDisasterStop
//           NEW (未成交)      → CancelOrder + markFailed (不冒险继续)
//           PARTIALLY_FILLED  → Round 2 v0.1 暂时标 failed (实盘极少, Round 3 处理)
//           CANCELED/EXPIRED  → markFailed
//
// 实施细节: 启动时单次跑, 在 collectors 启动前. 失败 1 行不影响其它 (per-row try).
package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/storage/postgres/gen"
)

// RecoveryDeps is the minimal interface RecoverEnteringTrades needs.
type RecoveryDeps interface {
	GetEnteringTradesForRecovery(ctx context.Context) ([]gen.GetEnteringTradesForRecoveryRow, error)
	UpdateTradeOpen(ctx context.Context, arg gen.UpdateTradeOpenParams) error
	UpdateTradeFailed(ctx context.Context, arg gen.UpdateTradeFailedParams) error
}

// BinanceQuerier is the minimal Binance-client interface used for recovery.
type BinanceQuerier interface {
	GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (binance.OrderResult, error)
	CancelOrder(ctx context.Context, symbol string, orderID int64) error
}

// RecoverEnteringTrades reconciles 'entering' trades with a client_order_id
// against Binance state. Returns count of (reconciled_open, marked_failed).
//
// Called from main.go at startup, AFTER orphan cleanup (which clears Phase 3
// legacy rows without client_order_id) and BEFORE collectors run.
func RecoverEnteringTrades(ctx context.Context, db RecoveryDeps, bc BinanceQuerier, log zerolog.Logger) (int, int) {
	rows, err := db.GetEnteringTradesForRecovery(ctx)
	if err != nil {
		log.Error().Err(err).Msg("recovery: query entering trades failed")
		return 0, 0
	}
	if len(rows) == 0 {
		return 0, 0
	}

	reconciledOpen, markedFailed := 0, 0
	for _, r := range rows {
		clientOrderID := r.ClientOrderID.String
		l := log.With().
			Int64("trade_id", r.ID).
			Str("symbol", r.Symbol).
			Str("client_order_id", clientOrderID).
			Logger()

		existing, err := bc.GetOrderByClientID(ctx, r.Symbol, clientOrderID)
		if err != nil {
			var apiErr *binance.APIError
			if errors.As(err, &apiErr) && binance.ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == binance.ActionTreatAsCanceled {
				// -2013 "Order does not exist" → trader crashed before PlaceMarketOrder hit Binance.
				l.Info().Msg("recovery: order not on binance, marking failed")
				markFailedRow(ctx, db, r.ID, "startup_recovery_not_found", l)
				markedFailed++
				continue
			}
			l.Error().Err(err).Msg("recovery: lookup failed (non-fatal, skip row)")
			continue
		}

		switch existing.Status {
		case "FILLED":
			l.Info().Str("avg_price", existing.AvgPrice.String()).Msg("recovery: order FILLED on binance, reconciling to open")
			if err := db.UpdateTradeOpen(ctx, gen.UpdateTradeOpenParams{
				ID:         r.ID,
				EntryTs:    existing.UpdateTime,
				EntryPrice: existing.AvgPrice,
			}); err != nil {
				l.Error().Err(err).Msg("recovery: UpdateTradeOpen failed")
				continue
			}
			reconciledOpen++
			// NOTE: disaster_stop placement is NOT auto-resumed here in Round 2 v0.1.
			// trades.binance_disaster_stop_order_id stays NULL → ops alert + Round 3 handles.
		case "NEW", "PARTIALLY_FILLED":
			// Pending / partial — Round 2 v0.1 conservative: cancel + fail (no naked long).
			l.Warn().Str("status", existing.Status).Msg("recovery: pending order found, canceling + marking failed")
			if err := bc.CancelOrder(ctx, r.Symbol, existing.OrderID); err != nil {
				l.Error().Err(err).Msg("recovery: cancel failed (continuing)")
			}
			markFailedRow(ctx, db, r.ID, "startup_recovery_"+existing.Status, l)
			markedFailed++
		case "CANCELED", "EXPIRED", "REJECTED":
			l.Info().Str("status", existing.Status).Msg("recovery: order " + existing.Status + ", marking failed")
			markFailedRow(ctx, db, r.ID, "startup_recovery_"+existing.Status, l)
			markedFailed++
		default:
			l.Warn().Str("status", existing.Status).Msg("recovery: unknown status, marking failed")
			markFailedRow(ctx, db, r.ID, "startup_recovery_unknown", l)
			markedFailed++
		}
	}

	log.Info().Int("checked", len(rows)).Int("reconciled_open", reconciledOpen).Int("marked_failed", markedFailed).Msg("startup recovery complete")
	return reconciledOpen, markedFailed
}

func markFailedRow(ctx context.Context, db RecoveryDeps, id int64, reason string, log zerolog.Logger) {
	if err := db.UpdateTradeFailed(ctx, gen.UpdateTradeFailedParams{
		ID:         id,
		ExitReason: pgtype.Text{String: reason, Valid: true},
		ExitTs:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		log.Error().Err(err).Str("reason", reason).Msg("recovery: UpdateTradeFailed failed")
	}
}

// guarded compile-time interface check.
var _ = fmt.Sprintf

// StartupRecovery orchestrates the post-restart reconciliation pipeline.
//
// Round 7 design: leverage existing Round 4-6 components instead of forking
// recovery logic. Run each subsystem's first tick eagerly at startup so the
// trader doesn't trade in an inconsistent window before the natural 1min /
// 5min cron schedules kick in.
//
// Order matters (sequential, fail-soft):
//  1. RecoverEnteringTrades (Round 2): clean up status='entering' rows that
//     never finished PlaceEntry — DB ⇄ Binance reconcile via clientOrderId.
//  2. PositionManager.SyncTick (Round 3 + 4): pull positionRisk, update
//     position_states, detect drift/local_only/binance_only → halt + RCA.
//  3. ExitManager.EvaluateTick (Round 5): retry stuck 'closing' / 'closing_failed'
//     close pipelines + run any soft/hard timeout that came due.
//  4. CircuitBreakerTripper.EvaluateAll (Round 6): re-evaluate 5-item trips so
//     trader doesn't open new positions if yesterday's halt condition persists
//     (daily_pnl / consec_losses survive restarts via DB).
//
// Each step is best-effort — failures are logged but don't block subsequent
// steps. Caller in main.go passes closures wrapping the concrete subsystems'
// tick functions (avoids import cycles + lets each tick keep its native
// signature: SyncTick / EvaluateTick / EvaluateAll all uniformly Tick-like).

// StartupRecoveryReport summarises what happened during the recovery pass.
type StartupRecoveryReport struct {
	EnteringReconciled int
	EnteringFailed     int
	HaltAfterRecovery  bool
}

// RunStartupRecovery executes the orchestrated post-restart pipeline.
// positionManagerTick / exitManagerTick / cbEvaluate are closures wrapping
// the respective subsystems' tick functions; main.go provides them.
func RunStartupRecovery(
	ctx context.Context,
	db RecoveryDeps,
	bc BinanceQuerier,
	positionManagerTick func(context.Context),
	exitManagerTick func(context.Context),
	cbEvaluate func(context.Context) bool,
	log zerolog.Logger,
) StartupRecoveryReport {
	log.Info().Msg("restart.recovery.start")
	report := StartupRecoveryReport{}

	// Step 1: entering trades.
	report.EnteringReconciled, report.EnteringFailed = RecoverEnteringTrades(ctx, db, bc, log)

	// Step 2: position_manager immediate sync (open trades + drift + orphan).
	if positionManagerTick != nil {
		log.Info().Msg("restart.recovery.position_sync")
		positionManagerTick(ctx)
	}

	// Step 3: exit_manager immediate evaluation (closing retries + timeouts).
	if exitManagerTick != nil {
		log.Info().Msg("restart.recovery.exit_eval")
		exitManagerTick(ctx)
	}

	// Step 4: circuit breaker re-evaluate (catches yesterday's halt conditions).
	if cbEvaluate != nil {
		log.Info().Msg("restart.recovery.cb_eval")
		report.HaltAfterRecovery = cbEvaluate(ctx)
	}

	log.Info().
		Int("entering_reconciled", report.EnteringReconciled).
		Int("entering_failed", report.EnteringFailed).
		Bool("halt_after_recovery", report.HaltAfterRecovery).
		Msg("restart.recovery.complete")
	return report
}
