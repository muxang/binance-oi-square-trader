package decision

import (
	"errors"

	"github.com/shopspring/decimal"

	"trader/internal/binance"
)

// SizingConfig holds the v0.1 仓位 constants per SPEC §仓位规则 L186-195.
type SizingConfig struct {
	FullMarginUSDT decimal.Decimal // 默认 50  (entered_full)
	HalfMarginUSDT decimal.Decimal // 默认 25  (entered_half)
	Leverage       int32           // 默认 10
}

func sizingDefaults(cfg SizingConfig) SizingConfig {
	if cfg.FullMarginUSDT.IsZero() {
		cfg.FullMarginUSDT = decimal.NewFromInt(50)
	}
	if cfg.HalfMarginUSDT.IsZero() {
		cfg.HalfMarginUSDT = decimal.NewFromInt(25)
	}
	if cfg.Leverage == 0 {
		cfg.Leverage = 10
	}
	return cfg
}

// SizingResult is the verdict + numeric breakdown for one signal's sizing.
// OK=true → all fields populated; OK=false → Reason set + diag fields still
// populated (caller logs full breakdown for debugging).
type SizingResult struct {
	OK             bool
	Reason         string          // empty when OK
	Quantity       decimal.Decimal // step-rounded, ready for Phase 4 真下单
	Margin         decimal.Decimal // 50 / 25 from cfg
	Notional       decimal.Decimal // ACTUAL = price × Quantity (post step-round)
	TargetNotional decimal.Decimal // = Margin × Leverage; diagnostic for step偏差
	EntryPrice     decimal.Decimal // = input price (caller picks klines close v0.1 / ticker live v0.2+)
	Leverage       int32
}

// Rejection reason labels (also used as metric outcome= label values).
const (
	SizingReasonInvalidDecision  = "invalid_decision"
	SizingReasonZeroPrice        = "zero_price"
	SizingReasonZeroStepSize     = "zero_step_size"
	SizingReasonBelowMinQty      = "below_min_qty"
	SizingReasonBelowMinNotional = "below_min_notional"
)

// SizeTrade computes target Quantity / Margin / Notional for one signal.
// Pure function — caller (engine.go) provides price + filters from
// SymbolService.GetTradingFilters / klines latest close (v0.1).
//
// Algorithm (SPEC §仓位规则 L186-195):
//
//	Margin         = 50 (full) | 25 (half)
//	TargetNotional = Margin × Leverage   (= 500 / 250 USDT)
//	QuantityRaw    = TargetNotional / price
//	Quantity       = floor(QuantityRaw / StepSize) × StepSize
//	Notional       = Quantity × price       (actual, may be < TargetNotional)
//
// Returns:
//
//	(Result{OK=true}, nil)              — sizing succeeded
//	(Result{OK=false, Reason=...}, nil) — business reject (caller log + skip)
//	(Result{}, err)                     — cfg invariant violation (programmer error)
//
// Reject reasons (Reason field) — caller uses for log + metric outcome label:
//
//	"invalid_decision"    decisionType not in {"entered_full", "entered_half"}
//	"zero_price"          price <= 0 (klines query returned 0 / no row)
//	"zero_step_size"      filters.StepSize <= 0 (incomplete exchangeInfo)
//	"below_min_qty"       Quantity < filters.MinQty (price too high vs margin)
//	"below_min_notional"  Notional < filters.MinNotional (after step-round drop)
func SizeTrade(
	decisionType string,
	price decimal.Decimal,
	filters binance.TradingFilters,
	cfg SizingConfig,
) (SizingResult, error) {
	cfg = sizingDefaults(cfg)
	// cfg invariants — programmer error, return error
	if cfg.Leverage <= 0 {
		return SizingResult{}, errors.New("invalid cfg: Leverage must be > 0")
	}
	if cfg.FullMarginUSDT.LessThanOrEqual(decimal.Zero) || cfg.HalfMarginUSDT.LessThanOrEqual(decimal.Zero) {
		return SizingResult{}, errors.New("invalid cfg: margins must be > 0")
	}

	var margin decimal.Decimal
	switch decisionType {
	case "entered_full":
		margin = cfg.FullMarginUSDT
	case "entered_half":
		margin = cfg.HalfMarginUSDT
	default:
		return SizingResult{
			OK: false, Reason: SizingReasonInvalidDecision,
			EntryPrice: price, Leverage: cfg.Leverage,
		}, nil
	}
	targetNotional := margin.Mul(decimal.NewFromInt32(cfg.Leverage))

	if price.LessThanOrEqual(decimal.Zero) {
		return SizingResult{
			OK: false, Reason: SizingReasonZeroPrice,
			Margin: margin, TargetNotional: targetNotional, Leverage: cfg.Leverage,
		}, nil
	}
	if filters.StepSize.LessThanOrEqual(decimal.Zero) {
		return SizingResult{
			OK: false, Reason: SizingReasonZeroStepSize,
			Margin: margin, TargetNotional: targetNotional, EntryPrice: price, Leverage: cfg.Leverage,
		}, nil
	}

	quantity := stepRoundDown(targetNotional.Div(price), filters.StepSize)
	notional := quantity.Mul(price)

	result := SizingResult{
		Quantity: quantity, Margin: margin,
		Notional: notional, TargetNotional: targetNotional,
		EntryPrice: price, Leverage: cfg.Leverage,
	}
	if quantity.LessThan(filters.MinQty) {
		result.Reason = SizingReasonBelowMinQty
		return result, nil
	}
	if notional.LessThan(filters.MinNotional) {
		result.Reason = SizingReasonBelowMinNotional
		return result, nil
	}
	result.OK = true
	return result, nil
}

// stepRoundDown returns floor(qty / step) × step using decimal.Decimal.
// For positive decimals Truncate(0) chops fractional == floor; safe for the
// caller's "round qty down to a multiple of stepSize" semantic.
func stepRoundDown(qty, step decimal.Decimal) decimal.Decimal {
	if step.IsZero() {
		return decimal.Zero
	}
	return qty.Div(step).Truncate(0).Mul(step)
}
