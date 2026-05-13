// Round R.3: orphan algo cleaner.
//
// mu real-money observation (2026-05-13): trader closes a position but the
// associated SELL reduceOnly Algo orders (disaster_stop / trail / TP1 / TP2)
// remain NEW/WORKING on Binance. They consume the per-account algo limit (200)
// and surface as noise in algo_polling. Position-side reduceOnly + closePosition
// guarantees they can never execute (qty=0), but Binance doesn't auto-cancel.
//
// 1min cron tick:
//   1. ListOpenAlgoOrders → all NEW/WORKING algos
//   2. GetPositionRisk("") → all non-zero positions
//   3. For each SELL reduceOnly algo whose symbol has NO position → cancel + audit
//
// Coexistence with other collectors:
//   · position_manager: DB-trade open + Binance qty=0 → orphan halt (trader-side recovery)
//   · orphan_algo_cleaner: Binance algo alive + Binance qty=0 → cancel (Binance-side cleanup)
// Different concerns: position_manager protects the trade state machine;
// this cleaner reclaims Binance algo slots and silences noise.
//
// Engineering decision: include WORKING status (not just NEW) — both are
// "armed and waiting", semantically equivalent for cleanup. A NEW algo whose
// symbol has no position will never transition to WORKING anyway.
package execution

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
)

// OrphanAlgoBinance is the binance surface (interface for test mocking).
type OrphanAlgoBinance interface {
	ListOpenAlgoOrders(ctx context.Context) ([]binance.AlgoOpenOrder, error)
	GetPositionRisk(ctx context.Context, symbol string) ([]binance.PositionRisk, error)
	CancelAlgoOrder(ctx context.Context, symbol string, algoID int64) error
}

// OrphanAlgoCleaner runs the 1min sweep.
type OrphanAlgoCleaner struct {
	bc    OrphanAlgoBinance
	db    *pgxpool.Pool
	log   zerolog.Logger
	nowFn func() time.Time
}

func NewOrphanAlgoCleaner(bc OrphanAlgoBinance, db *pgxpool.Pool, log zerolog.Logger) *OrphanAlgoCleaner {
	return &OrphanAlgoCleaner{bc: bc, db: db, log: log, nowFn: timez.NowUTC}
}

// ReconcileTick is the per-tick entry point.
func (oc *OrphanAlgoCleaner) ReconcileTick(ctx context.Context) {
	algos, err := oc.bc.ListOpenAlgoOrders(ctx)
	if err != nil {
		oc.log.Error().Err(err).Msg("orphan_algo.tick: ListOpenAlgoOrders failed")
		metrics.OrphanAlgoTickTotal.WithLabelValues("err").Inc()
		return
	}
	positions, err := oc.bc.GetPositionRisk(ctx, "")
	if err != nil {
		oc.log.Error().Err(err).Msg("orphan_algo.tick: GetPositionRisk failed")
		metrics.OrphanAlgoTickTotal.WithLabelValues("err").Inc()
		return
	}

	// Build position presence map (V3 already filters zero; double-check).
	hasPos := make(map[string]bool, len(positions))
	for _, p := range positions {
		if !p.PositionAmt.IsZero() {
			hasPos[p.Symbol] = true
		}
	}

	cancelled := 0
	for _, a := range algos {
		if !oc.isOrphan(a, hasPos) {
			continue
		}
		oc.cancelOrphan(ctx, a)
		cancelled++
	}
	oc.log.Info().
		Int("open_algos", len(algos)).
		Int("positions", len(positions)).
		Int("orphans_cancelled", cancelled).
		Msg("orphan_algo.tick")
	metrics.OrphanAlgoTickTotal.WithLabelValues("ok").Inc()
}

// isOrphan returns true when the algo should be cancelled.
// Criteria (must satisfy ALL):
//   · status in {NEW, WORKING} — terminal statuses already cleaned up
//   · SELL side AND (reduceOnly OR closePosition) — exit-only algos
//   · symbol has no open position (positionAmt zero or absent)
func (oc *OrphanAlgoCleaner) isOrphan(a binance.AlgoOpenOrder, hasPos map[string]bool) bool {
	if a.Status != "NEW" && a.Status != "WORKING" {
		return false
	}
	if a.Side != "SELL" {
		return false
	}
	if !a.ReduceOnly && !a.ClosePosition {
		return false
	}
	return !hasPos[a.Symbol]
}

func (oc *OrphanAlgoCleaner) cancelOrphan(ctx context.Context, a binance.AlgoOpenOrder) {
	log := oc.log.With().Int64("algo_id", a.AlgoID).Str("symbol", a.Symbol).
		Str("type", a.OrderType).Str("status", a.Status).Logger()

	if err := oc.bc.CancelAlgoOrder(ctx, a.Symbol, a.AlgoID); err != nil {
		log.Warn().Err(err).Msg("orphan_algo.cancel: failed (will retry next tick)")
		metrics.OrphanAlgoCancelFailures.WithLabelValues(a.Symbol).Inc()
		return
	}
	log.Info().Msg("orphan_algo.cancelled: position closed but algo still active")
	metrics.OrphanAlgoCancelled.WithLabelValues(a.Symbol, a.OrderType).Inc()

	// Audit log into admin_audit_log (Phase 5.2 Round 1 table). Non-fatal.
	if err := oc.writeAudit(ctx, a); err != nil {
		log.Warn().Err(err).Msg("orphan_algo.audit: insert failed (cancel succeeded)")
	}
}

// writeAudit inserts the cleanup event into admin_audit_log.
// operator='trader_auto' distinguishes from human admin actions.
// Nil pool → no-op (unit tests inject nil; prod always has pool).
func (oc *OrphanAlgoCleaner) writeAudit(ctx context.Context, a binance.AlgoOpenOrder) error {
	if oc.db == nil {
		return nil
	}
	const stmt = `
		INSERT INTO admin_audit_log
			(ts, operator, action_type, resource_type, resource_id,
			 previous_state, new_state, note)
		VALUES
			($1, 'trader_auto', 'orphan_algo_cleanup', 'algo', $2,
			 $3::jsonb, '{"cancelled": true}'::jsonb,
			 'position closed but algo still NEW/WORKING; orphan_algo_cleaner cancelled')
	`
	previousJSON := mustJSON(map[string]any{
		"symbol":         a.Symbol,
		"side":           a.Side,
		"type":           a.OrderType,
		"algo_status":    a.Status,
		"quantity":       a.Quantity.String(),
		"reduce_only":    a.ReduceOnly,
		"close_position": a.ClosePosition,
	})
	_, err := oc.db.Exec(ctx, stmt, oc.nowFn(), formatAlgoID(a.AlgoID), previousJSON)
	return err
}

// mustJSON serialises a map to JSON bytes; panics on failure (impossible for
// the well-typed inputs we feed it).
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return b
}

func formatAlgoID(id int64) string {
	return strconv.FormatInt(id, 10)
}
