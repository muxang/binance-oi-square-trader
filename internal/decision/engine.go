package decision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/storage/postgres/gen"
)

// SignalSource exposes the recent entered_* signals query (Round 1
// `GetRecentEnteredSignals`).
type SignalSource interface {
	GetRecentEnteredSignals(ctx context.Context, since time.Time) ([]gen.GetRecentEnteredSignalsRow, error)
}

// PriceReader exposes the latest close price for sizing.
// v0.1: klines latest 15m close. Phase 4: ticker/price live (adapter swap only).
type PriceReader interface {
	GetLatestClose(ctx context.Context, symbol string) (decimal.Decimal, error)
}

// FiltersSource exposes per-symbol trading filters (LOT_SIZE / MIN_NOTIONAL / etc).
type FiltersSource interface {
	GetTradingFilters(ctx context.Context, symbol string) (binance.TradingFilters, error)
}

// TradesWriter writes new entering-status trades (Phase 4 真下单 will UPDATE
// status='open' + entry_ts later).
type TradesWriter interface {
	InsertEnteringTrade(ctx context.Context, signalID int64, symbol, direction string, margin, notional decimal.Decimal, leverage int32) (int64, error)
}

// EngineDeps composes all 5 minimal interfaces Phase 3 decision engine needs
// (CLAUDE.md §18; FilterDeps is from filters.go = 3 inner interfaces).
type EngineDeps interface {
	FilterDeps
	SignalSource
	PriceReader
	FiltersSource
	TradesWriter
}

// EngineConfig wraps tick window + filter + sizing config.
type EngineConfig struct {
	SignalWindow time.Duration // 默认 5 min (cron 评估窗口)
	Filter       FilterConfig
	Sizing       SizingConfig
}

func engineDefaults(cfg EngineConfig) EngineConfig {
	if cfg.SignalWindow == 0 {
		cfg.SignalWindow = 5 * time.Minute
	}
	return cfg
}

// EvaluateResult is the verdict + diag fields per signal.
type EvaluateResult struct {
	SignalID int64
	Symbol   string
	Decision string       // entered_full / entered_half (input)
	Outcome  string       // metric outcome= label, see Outcome* consts
	Filter   FilterResult // filter step diag (always populated)
	Sizing   SizingResult // sizing step diag (populated only if filter passed)
}

// TickStats is the aggregate per-tick summary (caller logs this).
type TickStats struct {
	SignalsRead      int
	TradeEntering    int
	RejectedByFilter int
	RejectedBySizing int
	InternalError    int
}

// TickReport is RunTick's return — Stats for log + Results for per-outcome
// metric increments by caller.
type TickReport struct {
	Stats   TickStats
	Results []EvaluateResult
}

// Outcome label constants (= metric `trader_decision_evaluations_total{outcome=...}`)
const (
	OutcomeTradeEntering    = "trade_entering"
	OutcomeNoEnteredSignals = "no_entered_signals"
	OutcomeInternalError    = "internal_error"
	// rejected_<filter_reason> + sizing_<reason> derived dynamically
)

// EvaluateOne runs filter + sizing for one signal; read-only (no trade write).
// Caller (RunTick) inspects Outcome to decide write/skip.
//
// Outcomes:
//
//	OutcomeTradeEntering         — all gates passed, RunTick will write trade
//	"rejected_<filter_reason>"   — filter rejected, see Filter.Reason
//	"sizing_<sizing_reason>"     — sizing rejected, see Sizing.Reason
//
// Errors:
//
//	cfg invariants from filters/sizing → bubble up (caller treats as internal_error)
//	deps RPC errors → fail-safe rejection in respective step (NOT bubbled)
func EvaluateOne(
	ctx context.Context,
	signal gen.GetRecentEnteredSignalsRow,
	now time.Time,
	deps EngineDeps,
	cfg EngineConfig,
) (EvaluateResult, error) {
	cfg = engineDefaults(cfg)
	res := EvaluateResult{
		SignalID: signal.ID,
		Symbol:   signal.Symbol,
		Decision: signal.Decision,
	}

	filterRes, err := EvaluateGlobalFilters(ctx, signal.Symbol, now, deps, cfg.Filter)
	if err != nil {
		return res, fmt.Errorf("filters: %w", err)
	}
	res.Filter = filterRes
	if !filterRes.Passed {
		res.Outcome = "rejected_" + filterRes.Reason
		return res, nil
	}

	price, err := deps.GetLatestClose(ctx, signal.Symbol)
	if err != nil {
		// klines lookup failed → treat as zero_price sizing reject (data missing)
		res.Outcome = "sizing_" + SizingReasonZeroPrice
		return res, nil
	}
	filters, err := deps.GetTradingFilters(ctx, signal.Symbol)
	if err != nil {
		// SymbolService cache miss / refresh fail → treat as zero_step_size
		res.Outcome = "sizing_" + SizingReasonZeroStepSize
		return res, nil
	}

	sizingRes, err := SizeTrade(signal.Decision, price, filters, cfg.Sizing)
	if err != nil {
		return res, fmt.Errorf("sizing cfg: %w", err)
	}
	res.Sizing = sizingRes
	if !sizingRes.OK {
		res.Outcome = "sizing_" + sizingRes.Reason
		return res, nil
	}

	res.Outcome = OutcomeTradeEntering
	return res, nil
}

// RunTick is the cron-tick entry: read entered_* signals → loop EvaluateOne
// → write trade for trade_entering outcomes. Per-signal failures isolated
// (跨 signal 不杀 tick, Phase 1 模式).
//
// Top-level errors (signal source unavailable) bubble up so caller (collector
// runner) marks the whole tick failed.
func RunTick(
	ctx context.Context,
	now time.Time,
	deps EngineDeps,
	cfg EngineConfig,
) (TickReport, error) {
	cfg = engineDefaults(cfg)
	report := TickReport{}

	cutoff := now.Add(-cfg.SignalWindow)
	signals, err := deps.GetRecentEnteredSignals(ctx, cutoff)
	if err != nil {
		return report, fmt.Errorf("get recent signals: %w", err)
	}
	report.Stats.SignalsRead = len(signals)
	if len(signals) == 0 {
		return report, nil
	}

	report.Results = make([]EvaluateResult, 0, len(signals))
	for _, signal := range signals {
		result, err := EvaluateOne(ctx, signal, now, deps, cfg)
		if err != nil {
			result.Outcome = OutcomeInternalError
			report.Stats.InternalError++
			report.Results = append(report.Results, result)
			continue
		}
		switch {
		case result.Outcome == OutcomeTradeEntering:
			_, werr := deps.InsertEnteringTrade(ctx,
				result.SignalID, result.Symbol, "LONG",
				result.Sizing.Margin, result.Sizing.Notional, result.Sizing.Leverage)
			if werr != nil {
				result.Outcome = OutcomeInternalError
				report.Stats.InternalError++
			} else {
				report.Stats.TradeEntering++
			}
		case strings.HasPrefix(result.Outcome, "rejected_"):
			report.Stats.RejectedByFilter++
		case strings.HasPrefix(result.Outcome, "sizing_"):
			report.Stats.RejectedBySizing++
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}
