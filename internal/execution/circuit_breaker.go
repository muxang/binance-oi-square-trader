// Phase 4 Round 6: 5-item circuit breaker trip evaluation (real logic).
//
// SPEC §风控熔断 5 项 (Round 0 §4.4 设计 + mu Q4 A 自动 reset 24h):
//   1. daily_loss        daily_pnl_pct <= -5% of balance → halt 24h
//   2. consec_losses     ≥ 5 within 24h window           → halt 24h
//   3. btc_crash         BTC 30min drop ≥ 3%             → halt 24h
//   4. total_float_loss  sum(unrealized) <= -8% balance  → halt 24h
//   5. api_error_rate    binance API errors ≥ 3 in 1min  → halt 24h
//
// Trip 顺序 (cheapest first → expensive last):
//   1. api_error_rate (DB count only)
//   2. consec_losses + daily_loss (DB state read + balance API)
//   3. total_float_loss (positions snapshot + Redis × N)
//   4. btc_crash (klines query)
//
// 任一 trip 立即 return — 不并发评估, 不评估剩余.
// 已 halt 时跳过整个 trip 评估 (decision_engine.RunTick 已检查).
// halt_until = NOW + 24h. Round 2 maintainHaltState 自动 reset.

package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// Trip thresholds (SPEC + Round 0 §4.4).
const (
	dailyLossHaltPct       = 0.05 // 5% of balance
	consecutiveLossCount   = 5
	consecutiveLossWindow  = 24 * time.Hour
	btcCrashHaltPct        = 0.03 // 3% 30min drop
	totalFloatLossHaltPct  = 0.08 // 8% of balance
	apiErrorRateLimit      = 3    // per 1min
	apiErrorRateWindow     = 1 * time.Minute
	cbHaltDuration         = 24 * time.Hour
	btcCrash30MinBarCount  = 6 // 6 × 5min = 30min
	btcCrashKlineTimeframe = "5m"
)

// Trip type label values (also written to halt_rca.halt_type).
const (
	tripTypeDailyLoss      = "circuit_breaker_daily_loss"
	tripTypeConsecLosses   = "circuit_breaker_consec_losses"
	tripTypeBTCCrash       = "circuit_breaker_btc_crash"
	tripTypeTotalFloatLoss = "circuit_breaker_total_float_loss"
	tripTypeAPIError       = "circuit_breaker_api_error"
)

// CircuitBreakerDeps is the minimal DB surface needed for trip evaluation.
type CircuitBreakerDeps interface {
	GetCircuitBreakerStateForTrips(ctx context.Context) (gen.GetCircuitBreakerStateForTripsRow, error)
	TripGenericHalt(ctx context.Context, arg gen.TripGenericHaltParams) error
	InsertHaltRCA(ctx context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error)
	CountAPIErrorsSince(ctx context.Context, since time.Time) (int64, error)
	SumOpenUnrealizedSnapshot(ctx context.Context) ([]gen.SumOpenUnrealizedSnapshotRow, error)
	GetLatestKlines(ctx context.Context, arg gen.GetLatestKlinesParams) ([]gen.Kline, error)
}

// CircuitBreakerBinance is the minimal binance surface (account balance).
type CircuitBreakerBinance interface {
	GetUSDTBalance(ctx context.Context) (decimal.Decimal, error)
}

// CircuitBreakerTripper evaluates SPEC §风控熔断 5 项 trips.
type CircuitBreakerTripper struct {
	db    CircuitBreakerDeps
	bc    CircuitBreakerBinance
	rdb   *redis.Client
	log   zerolog.Logger
	nowFn func() time.Time
}

func NewCircuitBreakerTripper(db CircuitBreakerDeps, bc CircuitBreakerBinance, rdb *redis.Client, log zerolog.Logger) *CircuitBreakerTripper {
	return &CircuitBreakerTripper{db: db, bc: bc, rdb: rdb, log: log, nowFn: timez.NowUTC}
}

// EvaluateAll runs the 5 trips in fastest-to-slowest order. Returns true if
// any trip fired (caller knows halt is set). Returns false on no-trip OR on
// state-read failure (failure should not block trading; ops alerts via log).
//
// Idempotent: if circuit_breaker is already halted, returns immediately.
func (cb *CircuitBreakerTripper) EvaluateAll(ctx context.Context) bool {
	state, err := cb.db.GetCircuitBreakerStateForTrips(ctx)
	if err != nil {
		cb.log.Error().Err(err).Msg("circuit_breaker.tick: read state failed")
		return false
	}
	// Track gauge state.
	if state.TradingHalted {
		metrics.CircuitBreakerState.Set(1)
		return false // already halted, skip evaluation
	}
	metrics.CircuitBreakerState.Set(0)

	// Track operational gauges (visible regardless of trip).
	metrics.DailyPnlUSDT.Set(mustFloat(state.DailyPnl))
	metrics.ConsecutiveLossesGauge.Set(float64(state.ConsecutiveLosses))

	// 1. API error rate (cheapest — DB count only).
	if cb.tripAPIErrorRate(ctx) {
		return true
	}
	// 2. Consecutive losses (state already in hand).
	if cb.tripConsecutiveLosses(ctx, state) {
		return true
	}
	// 3. Daily loss (needs balance API).
	balance, balanceOK := cb.fetchBalance(ctx)
	if balanceOK && cb.tripDailyLoss(ctx, state.DailyPnl, balance) {
		return true
	}
	// 4. Total float loss (positions × Redis prices).
	if balanceOK && cb.tripTotalFloatLoss(ctx, balance) {
		return true
	}
	// 5. BTC crash (klines query).
	if cb.tripBTCCrash(ctx) {
		return true
	}
	return false
}

// fetchBalance returns (balance, ok). On error logs + returns (0, false) so
// caller skips balance-dependent trips for this tick.
func (cb *CircuitBreakerTripper) fetchBalance(ctx context.Context) (decimal.Decimal, bool) {
	bal, err := cb.bc.GetUSDTBalance(ctx)
	if err != nil {
		cb.log.Warn().Err(err).Msg("circuit_breaker: balance fetch failed (skip balance-dep trips)")
		return decimal.Zero, false
	}
	if !bal.IsPositive() {
		cb.log.Warn().Str("balance", bal.String()).Msg("circuit_breaker: zero balance (skip balance-dep trips)")
		return decimal.Zero, false
	}
	metrics.AccountBalanceUSDT.Set(mustFloat(bal))
	return bal, true
}

// tripAPIErrorRate counts api_errors rows in the last 1min window.
func (cb *CircuitBreakerTripper) tripAPIErrorRate(ctx context.Context) bool {
	since := cb.nowFn().Add(-apiErrorRateWindow)
	count, err := cb.db.CountAPIErrorsSince(ctx, since)
	if err != nil {
		cb.log.Warn().Err(err).Msg("circuit_breaker.api_error: count failed")
		return false
	}
	if count < apiErrorRateLimit {
		return false
	}
	cb.log.Warn().Int64("error_count", count).Int("threshold", apiErrorRateLimit).
		Float64("window_seconds", apiErrorRateWindow.Seconds()).
		Msg("circuit_breaker.trip.api_error")
	cb.fireTrip(ctx, tripTypeAPIError, map[string]any{
		"error_count":    count,
		"threshold":      apiErrorRateLimit,
		"window_seconds": apiErrorRateWindow.Seconds(),
	})
	return true
}

// tripConsecutiveLosses fires when count ≥ 5 AND last loss was within 24h.
func (cb *CircuitBreakerTripper) tripConsecutiveLosses(ctx context.Context, state gen.GetCircuitBreakerStateForTripsRow) bool {
	if int(state.ConsecutiveLosses) < consecutiveLossCount {
		return false
	}
	if !state.LastLossAt.Valid || cb.nowFn().Sub(state.LastLossAt.Time) > consecutiveLossWindow {
		// 5+ losses but last one was > 24h ago → not a fresh streak.
		return false
	}
	cb.log.Warn().Int16("count", state.ConsecutiveLosses).
		Time("last_loss_at", state.LastLossAt.Time).
		Msg("circuit_breaker.trip.consec_losses")
	cb.fireTrip(ctx, tripTypeConsecLosses, map[string]any{
		"count":        state.ConsecutiveLosses,
		"threshold":    consecutiveLossCount,
		"last_loss_at": state.LastLossAt.Time.Format(time.RFC3339),
		"window_hours": consecutiveLossWindow.Hours(),
	})
	return true
}

// tripDailyLoss fires when daily_pnl_pct ≤ -5% of balance.
func (cb *CircuitBreakerTripper) tripDailyLoss(ctx context.Context, dailyPnl, balance decimal.Decimal) bool {
	if !dailyPnl.IsNegative() {
		return false
	}
	ratio := dailyPnl.Div(balance) // negative
	threshold := decimal.NewFromFloat(-dailyLossHaltPct)
	if ratio.GreaterThan(threshold) {
		return false
	}
	cb.log.Warn().Str("daily_pnl", dailyPnl.String()).Str("balance", balance.String()).
		Str("ratio", ratio.String()).Float64("threshold", -dailyLossHaltPct).
		Msg("circuit_breaker.trip.daily_loss")
	cb.fireTrip(ctx, tripTypeDailyLoss, map[string]any{
		"daily_pnl":     dailyPnl.String(),
		"balance":       balance.String(),
		"ratio":         ratio.String(),
		"threshold_pct": -dailyLossHaltPct,
	})
	return true
}

// tripTotalFloatLoss fires when sum of unrealized PnL ≤ -8% balance.
func (cb *CircuitBreakerTripper) tripTotalFloatLoss(ctx context.Context, balance decimal.Decimal) bool {
	rows, err := cb.db.SumOpenUnrealizedSnapshot(ctx)
	if err != nil {
		cb.log.Warn().Err(err).Msg("circuit_breaker.total_float_loss: snapshot failed")
		return false
	}
	totalUnrealized := decimal.Zero
	for _, r := range rows {
		if !r.EntryPrice.Valid || !r.CurrentQty.Valid {
			continue
		}
		entry := decimalFromPgNumeric(r.EntryPrice)
		qty := decimalFromPgNumeric(r.CurrentQty)
		// Redis latest_price; if unavailable use entry (yields 0 — conservative).
		mark := entry
		if priceStr, err := cb.rdb.Get(ctx, "latest_price:"+r.Symbol).Result(); err == nil {
			if m, err := decimal.NewFromString(priceStr); err == nil {
				mark = m
			}
		}
		// LONG only Round 5+6: (mark - entry) × qty.
		totalUnrealized = totalUnrealized.Add(mark.Sub(entry).Mul(qty))
	}
	metrics.UnrealizedPnlTotalUSDT.Set(mustFloat(totalUnrealized))
	if !totalUnrealized.IsNegative() {
		return false
	}
	ratio := totalUnrealized.Div(balance)
	threshold := decimal.NewFromFloat(-totalFloatLossHaltPct)
	if ratio.GreaterThan(threshold) {
		return false
	}
	cb.log.Warn().Str("sum_unrealized", totalUnrealized.String()).Str("balance", balance.String()).
		Str("ratio", ratio.String()).Float64("threshold", -totalFloatLossHaltPct).
		Int("positions", len(rows)).
		Msg("circuit_breaker.trip.total_float_loss")
	cb.fireTrip(ctx, tripTypeTotalFloatLoss, map[string]any{
		"sum_unrealized": totalUnrealized.String(),
		"balance":        balance.String(),
		"ratio":          ratio.String(),
		"threshold_pct":  -totalFloatLossHaltPct,
		"positions":      len(rows),
	})
	return true
}

// tripBTCCrash fires when last 30min (6 × 5m bars) drop ≥ 3%.
func (cb *CircuitBreakerTripper) tripBTCCrash(ctx context.Context) bool {
	bars, err := cb.db.GetLatestKlines(ctx, gen.GetLatestKlinesParams{
		Symbol: "BTCUSDT", Timeframe: btcCrashKlineTimeframe, Limit: btcCrash30MinBarCount,
	})
	if err != nil || len(bars) < btcCrash30MinBarCount {
		return false
	}
	// bars sorted DESC: bars[0] = newest, bars[len-1] = oldest.
	newest := bars[0].Close
	oldest := bars[len(bars)-1].Open
	if !oldest.IsPositive() {
		return false
	}
	dropPct := oldest.Sub(newest).Div(oldest) // positive when dropping
	metrics.BTC30MinDropPct.Set(mustFloat(dropPct))
	threshold := decimal.NewFromFloat(btcCrashHaltPct)
	if dropPct.LessThan(threshold) {
		return false
	}
	cb.log.Warn().Str("drop_pct", dropPct.String()).Float64("threshold", btcCrashHaltPct).
		Str("start_price", oldest.String()).Str("current_price", newest.String()).
		Msg("circuit_breaker.trip.btc_crash")
	cb.fireTrip(ctx, tripTypeBTCCrash, map[string]any{
		"drop_pct":      dropPct.String(),
		"threshold":     btcCrashHaltPct,
		"start_price":   oldest.String(),
		"current_price": newest.String(),
		"window_min":    30,
	})
	return true
}

// fireTrip writes the halt + RCA atomically (best-effort).
func (cb *CircuitBreakerTripper) fireTrip(ctx context.Context, tripType string, context map[string]any) {
	now := cb.nowFn()
	haltUntil := pgtype.Timestamptz{Time: now.Add(cbHaltDuration), Valid: true}
	if err := cb.db.TripGenericHalt(ctx, gen.TripGenericHaltParams{
		HaltReason: pgtype.Text{String: tripType, Valid: true},
		HaltUntil:  haltUntil,
	}); err != nil {
		cb.log.Error().Err(err).Str("trip_type", tripType).Msg("circuit_breaker.trip: halt write failed")
		return
	}
	metrics.CircuitBreakerTripsTotal.WithLabelValues(tripType).Inc()
	metrics.CircuitBreakerState.Set(1)

	context["halt_until"] = haltUntil.Time.Format(time.RFC3339)
	ctxJSON, err := json.Marshal(context)
	if err != nil {
		ctxJSON = []byte(`{}`)
	}
	rca, err := cb.db.InsertHaltRCA(ctx, gen.InsertHaltRCAParams{
		HaltType:    tripType,
		ContextJson: ctxJSON,
	})
	if err != nil {
		cb.log.Error().Err(err).Str("trip_type", tripType).Msg("circuit_breaker.trip: rca write failed (halt still set)")
		return
	}
	metrics.HaltRCAPendingTotal.WithLabelValues(tripType).Inc()
	cb.log.Warn().
		Int64("halt_rca_id", rca.ID).
		Str("trip_type", tripType).
		Time("halt_until", haltUntil.Time).
		Msg("CIRCUIT_BREAKER_HALT")
}

// Suppress unused-import warning for fmt (kept for future debug).
var _ = fmt.Sprintf
