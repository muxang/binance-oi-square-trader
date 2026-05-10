package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// SignalRecord is the in-memory representation of one row in the signals
// table. Mirrors ARCH §7 / migration 0001_init signals schema.
type SignalRecord struct {
	Ts              time.Time
	Symbol          string
	OITriggered     bool
	OIData          OISurgeResult
	SquareHot       bool
	SquareData      SquareHotResult
	Decision        string // entered_full / entered_half / rejected
	RejectionReason string // empty when not rejected
}

// SignalDataReader exposes the 3 read paths Evaluate needs (CLAUDE.md §18 —
// minimal consumer-defined interfaces). Real impl in Round 5 wraps gen.Queries.
type SignalDataReader interface {
	GetOIHistory(ctx context.Context, symbol string, limit int) ([]decimal.Decimal, error)
	GetHashtagHistory(ctx context.Context, symbol string, limit int) ([]decimal.Decimal, error)
	GetKlinesCloseNowAndPrior(ctx context.Context, symbol string, priorAgo time.Duration) (now, prior decimal.Decimal, err error)
}

// SignalSink writes signals.
type SignalSink interface {
	InsertSignal(ctx context.Context, rec SignalRecord) error
}

// SignalDataAccess composes reader + sink for Evaluate's signature.
type SignalDataAccess interface {
	SignalDataReader
	SignalSink
}

// CompoundConfig wires algorithm thresholds + IO query limits.
type CompoundConfig struct {
	OIHistoryLimit      int           // 默认 15 (LookbackPeriods 10 + buffer)
	HashtagHistoryLimit int           // 默认 100 (24h × 4/h = 96 + buffer)
	PriorAgo            time.Duration // 默认 60min (cond 4 不接顶保护)
	OISurgeCfg          OISurgeConfig
	SquareHotCfg        SquareHotConfig
}

func compoundDefaults(cfg CompoundConfig) CompoundConfig {
	if cfg.OIHistoryLimit == 0 {
		cfg.OIHistoryLimit = 15
	}
	if cfg.HashtagHistoryLimit == 0 {
		cfg.HashtagHistoryLimit = 100
	}
	if cfg.PriorAgo == 0 {
		cfg.PriorAgo = 60 * time.Minute
	}
	return cfg
}

// Evaluate computes and writes one signal record for one symbol. Read failures
// → return error, no write. Write failure → return wrapped error. Caller
// (signal_engine) loops over pool symbols and isolates per-symbol errors.
//
// Decision logic (SPEC §入场决策 L77-82):
//
//	OI 触发 + hot=true   → entered_full
//	OI 触发 + hot=false  → entered_half
//	OI 不触发            → rejected (rejection_reason = OISurgeResult.FailedReason)
func Evaluate(
	ctx context.Context,
	symbol string,
	ts time.Time,
	deps SignalDataAccess,
	cfg CompoundConfig,
) (SignalRecord, error) {
	cfg = compoundDefaults(cfg)

	oiSeries, err := deps.GetOIHistory(ctx, symbol, cfg.OIHistoryLimit)
	if err != nil {
		return SignalRecord{}, fmt.Errorf("get oi history: %w", err)
	}
	hashtagSeries, err := deps.GetHashtagHistory(ctx, symbol, cfg.HashtagHistoryLimit)
	if err != nil {
		return SignalRecord{}, fmt.Errorf("get hashtag history: %w", err)
	}
	closeNow, closePrior, err := deps.GetKlinesCloseNowAndPrior(ctx, symbol, cfg.PriorAgo)
	if err != nil {
		return SignalRecord{}, fmt.Errorf("get klines: %w", err)
	}

	// Algo layer: pure functions.
	oiResult, err := OISurge(oiSeries, closeNow, closePrior, cfg.OISurgeCfg)
	if err != nil {
		return SignalRecord{}, fmt.Errorf("oi_surge: %w", err)
	}
	hotResult, err := SquareHot(hashtagSeries, cfg.SquareHotCfg)
	if err != nil {
		return SignalRecord{}, fmt.Errorf("square_hot: %w", err)
	}

	rec := SignalRecord{
		Ts:          ts,
		Symbol:      symbol,
		OITriggered: oiResult.Triggered,
		OIData:      oiResult,
		SquareHot:   hotResult.Hot,
		SquareData:  hotResult,
	}
	switch {
	case oiResult.Triggered && hotResult.Hot:
		rec.Decision = "entered_full"
	case oiResult.Triggered && !hotResult.Hot:
		rec.Decision = "entered_half"
	default:
		rec.Decision = "rejected"
		// rejection_reason 仅在 OI 不触发时填 (OISurgeResult.FailedReason)。
		// OI 触发但 hot=false 走 entered_half 不算 rejected, RejectionReason 留空。
		rec.RejectionReason = oiResult.FailedReason
	}

	if err := deps.InsertSignal(ctx, rec); err != nil {
		return rec, fmt.Errorf("insert signal: %w", err)
	}
	return rec, nil
}

// MarshalOIDataJSON returns the JSONB bytes for signals.oi_data.
// Exposed for Round 5 adapter (gen.InsertSignalParams.OiData []byte).
func MarshalOIDataJSON(r OISurgeResult) ([]byte, error) {
	return json.Marshal(r)
}

// MarshalSquareDataJSON returns the JSONB bytes for signals.square_data.
func MarshalSquareDataJSON(r SquareHotResult) ([]byte, error) {
	return json.Marshal(r)
}
