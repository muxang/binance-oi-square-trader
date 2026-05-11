// Phase 3 决策引擎 collector wrapper.
//
// 数据流 (per ARCH §6 + 46c616f channel→DB note):
//
//	signals (Phase 2 写入) → decision_engine 读 entered_* signals 5min 窗
//	  → EvaluateOne (filters Step 1-3 + sizing) → trades.entering (Phase 3 写)
//	  → Phase 4 真下单 update entry_ts + status='open'
//
// 评估范围: signals.decision IN ('entered_full','entered_half') 5min 内.
// Phase 3 v0.1 真数据时刻预期: 0 entered (Phase 2 v0.1 PARTIAL 状态全 rejected),
// RunTick 走 no_entered_signals 路径 → trade_entering 真路径留 v0.2 forward
// (部署外网代理稳定后真 entered 出现).

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/decision"
	"trader/internal/execution"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

const (
	decisionEngineBTCRedisKey     = "btc_5m_change"
	decisionEngineKlinesTimeframe = "15m"
	decisionEngineKlinesLimit     = 1
)

type DecisionEngineConfig struct {
	PerTickTimeout time.Duration
	EngineCfg      decision.EngineConfig
}

func decisionEngineDefaults(cfg DecisionEngineConfig) DecisionEngineConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 4 * time.Minute
	}
	return cfg
}

// DecisionEngineCollector implements Phase 3 决策引擎, registered in main.go
// with cron */5 (5min cron, aligned with signal_engine 5min decision window).
// Phase 4: executor is wired in to PlaceEntry for trade_entering outcomes.
type DecisionEngineCollector struct {
	deps     decision.EngineDeps
	executor *execution.Executor          // Phase 4 entry executor; nil in Phase 3-only deploys
	cb       *execution.CircuitBreakerTripper // Phase 4 Round 6 5-item trip; nil → skipped
	log      zerolog.Logger
	cfg      DecisionEngineConfig
	nowFunc  func() time.Time
}

func NewDecisionEngineCollector(
	queries *gen.Queries, rdb *redis.Client, symbols *binance.SymbolService,
	executor *execution.Executor, cb *execution.CircuitBreakerTripper,
	log zerolog.Logger, cfg DecisionEngineConfig,
) *DecisionEngineCollector {
	cfg = decisionEngineDefaults(cfg)
	c := &DecisionEngineCollector{log: log, cfg: cfg, nowFunc: timez.NowUTC, executor: executor, cb: cb}
	c.deps = &decisionDataAccess{queries: queries, redis: rdb, symbols: symbols, log: log}
	return c
}

func (c *DecisionEngineCollector) Name() string { return "decision_engine" }

func (c *DecisionEngineCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	// Phase 4 Round 6: 5-item circuit breaker trip evaluation BEFORE RunTick.
	// If any trip fires, decision engine still runs but stepCircuitBreaker
	// (filters.go) will short-circuit rejects (trading_halted=true).
	if c.cb != nil {
		c.cb.EvaluateAll(tickCtx)
	}

	report, err := decision.RunTick(tickCtx, c.nowFunc(), c.deps, c.cfg.EngineCfg)
	if err != nil {
		return fmt.Errorf("RunTick: %w", err)
	}

	// Per-result metric + log (no_entered short-circuit when SignalsRead=0)
	if report.Stats.SignalsRead == 0 {
		metrics.DecisionEvaluationsTotal.WithLabelValues(decision.OutcomeNoEnteredSignals).Inc()
		c.log.Info().Msg("decision_engine tick complete: no_entered_signals")
		return nil
	}
	for _, r := range report.Results {
		metrics.DecisionEvaluationsTotal.WithLabelValues(r.Outcome).Inc()
		// sizing deviation histogram: only when trade_entering (sizing OK reached)
		if r.Outcome == decision.OutcomeTradeEntering && r.Sizing.TargetNotional.IsPositive() {
			deviation := r.Sizing.TargetNotional.Sub(r.Sizing.Notional).Div(r.Sizing.TargetNotional).Mul(decimal.NewFromInt(100))
			pct, _ := deviation.Float64()
			metrics.DecisionSizingDeviationPct.WithLabelValues(symbolClass(r.Sizing.EntryPrice)).Observe(pct)
		}
	}
	c.log.Info().
		Int("signals_read", report.Stats.SignalsRead).
		Int("trade_entering", report.Stats.TradeEntering).
		Int("rejected_by_filter", report.Stats.RejectedByFilter).
		Int("rejected_by_sizing", report.Stats.RejectedBySizing).
		Int("internal_error", report.Stats.InternalError).
		Msg("decision_engine tick complete")

	// Phase 4: fire PlaceEntry for each trade_entering outcome. Uses errgroup
	// so goroutines are tracked (CLAUDE.md §17). Background ctx so PlaceEntry
	// is not bound to the 4min tick timeout; PlaceEntry has its own 60s deadline.
	if c.executor != nil && report.Stats.TradeEntering > 0 {
		var eg errgroup.Group
		for _, r := range report.Results {
			if r.Outcome != decision.OutcomeTradeEntering || r.TradeID == 0 {
				continue
			}
			r := r
			eg.Go(func() error {
				c.executor.PlaceEntry(context.Background(),
					r.TradeID, r.SignalID, r.Symbol, r.Decision,
					r.Sizing.Quantity, r.Sizing.Margin, r.Sizing.Notional,
					r.Sizing.EntryPrice, r.Sizing.TickSize, r.Sizing.Leverage)
				return nil // PlaceEntry handles errors internally
			})
		}
		if err := eg.Wait(); err != nil {
			// PlaceEntry always returns nil; this branch is unreachable in practice.
			c.log.Error().Err(err).Msg("decision_engine: executor eg error")
		}
	}

	return nil
}

// symbolClass buckets price into 3 enum values for sizing_deviation histogram
// (per Round 4 #4 决策, cardinality 3).
func symbolClass(price decimal.Decimal) string {
	switch {
	case price.LessThan(decimal.NewFromInt(1)):
		return "low_price"
	case price.GreaterThanOrEqual(decimal.NewFromInt(1000)):
		return "high_price"
	default:
		return "mid_price"
	}
}

// --- adapter: decision.EngineDeps ⇐ gen.Queries + redis + SymbolService ---

type decisionDataAccess struct {
	queries *gen.Queries
	redis   *redis.Client
	symbols *binance.SymbolService
	log     zerolog.Logger
}

// FilterDeps via 3 inner interfaces

func (a *decisionDataAccess) GetBTCRegime(ctx context.Context) (decimal.Decimal, error) {
	val, err := a.redis.Get(ctx, decisionEngineBTCRedisKey).Result()
	if errors.Is(err, redis.Nil) {
		return decimal.Zero, fmt.Errorf("btc_5m_change not in redis: %w", err)
	}
	if err != nil {
		return decimal.Zero, err
	}
	// btc_regime.go writes JSON {drop_pct, open, close, ...}; parse drop_pct only
	var payload struct {
		DropPct decimal.Decimal `json:"drop_pct"`
	}
	if err := json.Unmarshal([]byte(val), &payload); err != nil {
		return decimal.Zero, fmt.Errorf("parse btc_5m_change: %w", err)
	}
	return payload.DropPct, nil
}

func (a *decisionDataAccess) GetState(ctx context.Context) (gen.CircuitBreakerState, error) {
	return a.queries.GetCircuitBreakerState(ctx)
}

func (a *decisionDataAccess) TripBTCHalt(ctx context.Context, until, crashTs time.Time) error {
	return a.queries.TripBTCHalt(ctx, gen.TripBTCHaltParams{
		HaltUntil:      pgtype.Timestamptz{Time: until, Valid: true},
		LastBtcCrashTs: pgtype.Timestamptz{Time: crashTs, Valid: true},
	})
}

func (a *decisionDataAccess) ResetHalt(ctx context.Context) error {
	return a.queries.ResetHalt(ctx)
}

func (a *decisionDataAccess) CountActive(ctx context.Context) (int64, error) {
	return a.queries.CountActiveTrades(ctx)
}

func (a *decisionDataAccess) HasRecent24hAttempt(ctx context.Context, symbol string, cutoff time.Time) (bool, error) {
	return a.queries.HasRecent24hAttemptForSymbol(ctx, gen.HasRecent24hAttemptForSymbolParams{
		Symbol: symbol, Ts: cutoff,
	})
}

// SignalSource

func (a *decisionDataAccess) GetRecentEnteredSignals(ctx context.Context, since time.Time) ([]gen.GetRecentEnteredSignalsRow, error) {
	return a.queries.GetRecentEnteredSignals(ctx, since)
}

// PriceReader — v0.1 klines latest 15m close. Phase 4 切 ticker/price.

func (a *decisionDataAccess) GetLatestClose(ctx context.Context, symbol string) (decimal.Decimal, error) {
	rows, err := a.queries.GetLatestKlines(ctx, gen.GetLatestKlinesParams{
		Symbol: symbol, Timeframe: decisionEngineKlinesTimeframe, Limit: decisionEngineKlinesLimit,
	})
	if err != nil {
		return decimal.Zero, err
	}
	if len(rows) == 0 {
		return decimal.Zero, fmt.Errorf("no klines for %s timeframe %s", symbol, decisionEngineKlinesTimeframe)
	}
	return rows[0].Close, nil
}

// FiltersSource — wraps SymbolService.GetTradingFilters (Round 1, 1h cache)

func (a *decisionDataAccess) GetTradingFilters(ctx context.Context, symbol string) (binance.TradingFilters, error) {
	return a.symbols.GetTradingFilters(ctx, symbol)
}

// TradesWriter

func (a *decisionDataAccess) InsertEnteringTrade(ctx context.Context, signalID int64, symbol, direction, clientOrderID string, margin, notional decimal.Decimal, leverage int32) (int64, error) {
	return a.queries.InsertEnteringTradeWithClientID(ctx, gen.InsertEnteringTradeWithClientIDParams{
		SignalID:      pgtype.Int8{Int64: signalID, Valid: true},
		Symbol:        symbol,
		Direction:     direction,
		Margin:        margin,
		Notional:      notional,
		Leverage:      int16(leverage),
		ClientOrderID: clientOrderID,
	})
}
