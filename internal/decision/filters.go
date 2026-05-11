// Package decision implements Phase 3 决策引擎 — read entered_full/entered_half
// signals from Phase 2, run SPEC §全局过滤, size positions, write trades
// (status='entering'). Phase 4 真下单 after.
package decision

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"trader/internal/pkg/metrics"
	"trader/internal/storage/postgres/gen"
)

// BTCRegimeReader exposes the cached 5min-window BTC regime (Redis
// `btc_5m_change` written by T6). dropPct > 0 = falling; dropPct > 0.03
// triggers SPEC §风控熔断 BTC trip (per SPEC §全局过滤 L206 + §风控熔断 L278).
type BTCRegimeReader interface {
	GetBTCRegime(ctx context.Context) (dropPct decimal.Decimal, err error)
}

// CircuitBreakerStore reads + mutates the single-row `circuit_breaker_state`.
// Phase 3 v0.1 only manages the BTC trip + auto-reset; daily_pnl /
// consecutive_losses / api_errors trips are Phase 3 v0.2 / Phase 4.
type CircuitBreakerStore interface {
	GetState(ctx context.Context) (gen.CircuitBreakerState, error)
	TripBTCHalt(ctx context.Context, until, crashTs time.Time) error
	ResetHalt(ctx context.Context) error
}

// TradesReader exposes the active-count + per-symbol 24h-window queries
// the global filters need.
type TradesReader interface {
	CountActive(ctx context.Context) (int64, error)
	HasRecent24hAttempt(ctx context.Context, symbol string, cutoff time.Time) (bool, error)
}

// FilterDeps composes the 3 minimal-purpose interfaces (CLAUDE.md §18).
type FilterDeps interface {
	BTCRegimeReader
	CircuitBreakerStore
	TradesReader
}

// FilterResult is the outcome of EvaluateGlobalFilters. Reason is empty
// when Passed=true; otherwise one of the labelled rejection constants.
type FilterResult struct {
	Passed bool
	Reason string
}

// Rejection reason labels (also used as metric outcome= label values).
const (
	ReasonBTCCrash             = "btc_5m_crash"
	ReasonBTCRegimeUnavailable = "btc_regime_unavailable"
	ReasonCBStateUnavailable   = "circuit_breaker_state_unavailable"
	ReasonAlreadyHalted        = "already_halted"
	ReasonPositionLimit        = "position_limit"
	ReasonRecent24hTrade       = "recent_24h_trade"
	ReasonCountUnavailable     = "trades_count_unavailable"
	Reason24hLookupUnavailable = "trades_24h_lookup_unavailable"
)

// FilterConfig holds the v0.1 thresholds. v0.2 may calibrate after forward.
type FilterConfig struct {
	BTCDropThreshold decimal.Decimal // 默认 0.03 (SPEC §全局过滤 L206 + §风控熔断 L278)
	HaltDuration     time.Duration   // 默认 30 min (SPEC L278)
	PositionLimit    int             // 默认 5 (SPEC §仓位规则 L190)
	Recent24hWindow  time.Duration   // 默认 24h (SPEC L191/L205)
}

func filterDefaults(cfg FilterConfig) FilterConfig {
	if cfg.BTCDropThreshold.IsZero() {
		cfg.BTCDropThreshold = decimal.NewFromFloat(0.03)
	}
	if cfg.HaltDuration == 0 {
		cfg.HaltDuration = 30 * time.Minute
	}
	if cfg.PositionLimit == 0 {
		cfg.PositionLimit = 5
	}
	if cfg.Recent24hWindow == 0 {
		cfg.Recent24hWindow = 24 * time.Hour
	}
	return cfg
}

// EvaluateGlobalFilters runs the SPEC §全局过滤 v0.1 subset (3 of 10):
//
//	#1 熔断 (BTC trip + auto-reset, folds in #5 BTC 5min)
//	#3 持仓数 < PositionLimit
//	#4 该 symbol 24h 内未尝试入场
//
// Returns FilterResult{Passed:true} only if all 3 pass; otherwise first
// failure short-circuits with a labelled Reason. Deps errors surface as
// rejection (fail-safe: data missing == do not enter, per SPEC §出场逻辑 spirit
// "数据不全不交易").
//
// Phase 3 v0.1 unimplemented filters (留 v0.2 / Phase 4):
//
//	#2 当日累亏   #6 连续亏损暂停期
//	#7 持仓总浮亏 #8 API 错误率
//	#9 symbol 在监控池 #10 minNotional + 10x
func EvaluateGlobalFilters(
	ctx context.Context,
	symbol string,
	now time.Time,
	deps FilterDeps,
	cfg FilterConfig,
) (FilterResult, error) {
	cfg = filterDefaults(cfg)
	if r := stepCircuitBreaker(ctx, now, deps, cfg); !r.Passed {
		return r, nil
	}
	if r := stepPositionLimit(ctx, deps, cfg); !r.Passed {
		return r, nil
	}
	if r := stepRecent24h(ctx, symbol, now, deps, cfg); !r.Passed {
		return r, nil
	}
	return FilterResult{Passed: true}, nil
}

// maintainHaltState is the Round 2 fix for: when there are no entered signals
// to evaluate, RunTick short-circuits and stepCircuitBreaker never runs, so
// halts never auto-reset. This function runs the halt_until-expired auto-reset
// independently of whether any signals exist this tick.
//
// Errors are swallowed: this is "best-effort housekeeping" — if state read
// fails, the next tick will retry. Auto-reset failure to actually clear the
// halt is logged via metric (-).
func maintainHaltState(ctx context.Context, now time.Time, deps FilterDeps) {
	state, err := deps.GetState(ctx)
	if err != nil {
		return
	}
	if !state.TradingHalted || !state.HaltUntil.Valid || !now.After(state.HaltUntil.Time) {
		return
	}
	if rerr := deps.ResetHalt(ctx); rerr != nil {
		return
	}
	haltType := "unknown"
	if state.HaltReason.Valid && state.HaltReason.String != "" {
		haltType = state.HaltReason.String
	}
	metrics.HaltAutoResetTotal.WithLabelValues(haltType).Inc()
}

// stepCircuitBreaker — SPEC §全局过滤 #1 + folded #5 (BTC crash trip).
//
//	1a. Read state. Unavailable → fail-safe reject.
//	1b. If currently halted but halt_until expired → ResetHalt + continue.
//	1c. If still halted → reject with halt_reason.
//	1d. Read BTC regime. Unavailable → fail-safe reject.
//	1e. If dropPct > threshold (strict >, per SPEC L206 "≤ 3% 通过") →
//	    TripBTCHalt + reject. Trip failure is non-fatal but reject is fatal.
func stepCircuitBreaker(ctx context.Context, now time.Time, deps FilterDeps, cfg FilterConfig) FilterResult {
	state, err := deps.GetState(ctx)
	if err != nil {
		return FilterResult{Passed: false, Reason: ReasonCBStateUnavailable}
	}
	if state.TradingHalted && state.HaltUntil.Valid && now.After(state.HaltUntil.Time) {
		if rerr := deps.ResetHalt(ctx); rerr != nil {
			// ResetHalt failed — treat as still halted (defensive: don't allow
			// new entries when state machine is in unknown state).
			reason := ReasonAlreadyHalted
			if state.HaltReason.Valid && state.HaltReason.String != "" {
				reason = state.HaltReason.String
			}
			return FilterResult{Passed: false, Reason: reason}
		}
		// Round 2: count auto-reset by halt_type label for ops visibility.
		haltType := "unknown"
		if state.HaltReason.Valid && state.HaltReason.String != "" {
			haltType = state.HaltReason.String
		}
		metrics.HaltAutoResetTotal.WithLabelValues(haltType).Inc()
		state.TradingHalted = false
	}
	if state.TradingHalted {
		reason := ReasonAlreadyHalted
		if state.HaltReason.Valid && state.HaltReason.String != "" {
			reason = state.HaltReason.String
		}
		return FilterResult{Passed: false, Reason: reason}
	}
	dropPct, err := deps.GetBTCRegime(ctx)
	if err != nil {
		return FilterResult{Passed: false, Reason: ReasonBTCRegimeUnavailable}
	}
	if dropPct.GreaterThan(cfg.BTCDropThreshold) {
		// Trip persists for cfg.HaltDuration; even if Trip RPC fails, reject
		// this evaluation (fail-safe: BTC really crashed, do not enter).
		_ = deps.TripBTCHalt(ctx, now.Add(cfg.HaltDuration), now)
		return FilterResult{Passed: false, Reason: ReasonBTCCrash}
	}
	return FilterResult{Passed: true}
}

// stepPositionLimit — SPEC §全局过滤 #3 (持仓数 < 5).
// CountActive 包含 'entering' / 'open' / 'partial' (Round 1 SQL) — Phase 3
// v0.1 写入 'entering' 也占位, 跨 Phase 一致.
func stepPositionLimit(ctx context.Context, deps FilterDeps, cfg FilterConfig) FilterResult {
	count, err := deps.CountActive(ctx)
	if err != nil {
		return FilterResult{Passed: false, Reason: ReasonCountUnavailable}
	}
	if count >= int64(cfg.PositionLimit) {
		return FilterResult{Passed: false, Reason: ReasonPositionLimit}
	}
	return FilterResult{Passed: true}
}

// stepRecent24h — SPEC §全局过滤 #4 (24h 不二次入场).
// Phase 3 v0.1: HasRecent24hAttempt 走 signals.ts JOIN (trades.entry_ts NULL
// for 'entering'). Phase 4 真下单 entry_ts 填 → 切回 entry_ts 路径.
func stepRecent24h(ctx context.Context, symbol string, now time.Time, deps FilterDeps, cfg FilterConfig) FilterResult {
	cutoff := now.Add(-cfg.Recent24hWindow)
	has, err := deps.HasRecent24hAttempt(ctx, symbol, cutoff)
	if err != nil {
		return FilterResult{Passed: false, Reason: Reason24hLookupUnavailable}
	}
	if has {
		return FilterResult{Passed: false, Reason: ReasonRecent24hTrade}
	}
	return FilterResult{Passed: true}
}
