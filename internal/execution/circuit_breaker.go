// Phase 4 Round 4 Step 3: 5-item circuit breaker trip placeholders.
//
// SPEC §风控熔断 5 项 (Round 0 §4.4 设计):
//   1. Daily loss > 5% of capital      → 24h halt    (Round 6)
//   2. Consecutive losses ≥ 5           → 1h halt     (Round 6)
//   3. BTC 5min crash > 3%              → 30min halt  (Phase 3 已实施)
//   4. Total unrealized float > 10%     → 1h halt     (Round 7)
//   5. API error rate > 30% in 5min     → 5min halt   (Round 7)
//
// Round 4 占位: 函数签名 + return early. Round 5-7 各自实施触发逻辑.
// 集成点: PositionManager.SyncTick / decision_engine / Round 5 exit logic
// 调用这些 trip helpers. 复用 Round 2/4 的 halt_until 自动 reset 路径.

package execution

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/shopspring/decimal"
)

// CircuitBreakerTripper exposes the 5 SPEC-mandated halt triggers as a single
// interface. Round 4 placeholders log + return; Round 5-7 fill in real
// trip logic. PositionManager / decision_engine / exit logic owners call into
// this surface so the trip API is stable as implementations land.
type CircuitBreakerTripper struct {
	log zerolog.Logger
}

func NewCircuitBreakerTripper(log zerolog.Logger) *CircuitBreakerTripper {
	return &CircuitBreakerTripper{log: log}
}

// TripDailyLoss is SPEC §风控熔断 #1: cumulative daily realized PnL falls
// below -5% of starting capital → halt 24h. Round 6 implements.
func (cb *CircuitBreakerTripper) TripDailyLoss(_ context.Context, dailyPnl, startCapital decimal.Decimal) {
	cb.log.Debug().
		Str("daily_pnl", dailyPnl.String()).
		Str("start_capital", startCapital.String()).
		Msg("circuit_breaker.daily_loss: placeholder (Round 6)")
}

// TripConsecutiveLosses is SPEC §风控熔断 #2: 5 consecutive losing closes →
// halt 1h. Round 6 implements (needs Round 5 exit logic to count losses).
func (cb *CircuitBreakerTripper) TripConsecutiveLosses(_ context.Context, count int) {
	cb.log.Debug().Int("consecutive_losses", count).Msg("circuit_breaker.consecutive_losses: placeholder (Round 6)")
}

// TripBTCCrash is SPEC §风控熔断 #3: BTC 5min drop > 3% → halt 30min.
// Phase 3 v0.1 ALREADY implements this in decision/filters.go (stepCircuitBreaker
// calls deps.TripBTCHalt). Placeholder here for surface uniformity.
func (cb *CircuitBreakerTripper) TripBTCCrash(_ context.Context, dropPct decimal.Decimal) {
	cb.log.Debug().Str("drop_pct", dropPct.String()).
		Msg("circuit_breaker.btc_crash: already wired via decision/filters.go (Phase 3)")
}

// TripTotalFloatLoss is SPEC §风控熔断 #4: aggregate unrealized PnL across
// all open positions < -10% of starting capital → halt 1h. Round 7 implements
// (needs Round 3 position_manager unrealized aggregation).
func (cb *CircuitBreakerTripper) TripTotalFloatLoss(_ context.Context, floatLoss, startCapital decimal.Decimal) {
	cb.log.Debug().
		Str("float_loss", floatLoss.String()).
		Str("start_capital", startCapital.String()).
		Msg("circuit_breaker.total_float_loss: placeholder (Round 7)")
}

// TripAPIErrorRate is SPEC §风控熔断 #5: API error rate > 30% in 5min window →
// halt 5min. Round 7 implements (needs api_errors table aggregator).
func (cb *CircuitBreakerTripper) TripAPIErrorRate(_ context.Context, errorRate float64) {
	cb.log.Debug().Float64("error_rate", errorRate).
		Msg("circuit_breaker.api_error_rate: placeholder (Round 7)")
}
