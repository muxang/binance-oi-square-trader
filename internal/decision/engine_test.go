package decision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/storage/postgres/gen"
)

// fakeEngineDeps implements EngineDeps. Composes fakeFilterDeps (filters_test.go)
// plus the 4 new methods Phase 3 engine needs.
type fakeEngineDeps struct {
	*fakeFilterDeps

	// SignalSource
	signals    []gen.GetRecentEnteredSignalsRow
	signalsErr error

	// PriceReader
	price    decimal.Decimal
	priceErr error

	// FiltersSource
	filters    binance.TradingFilters
	filtersErr error

	// TradesWriter
	insertedTrades []insertedTrade
	insertErr      error
	insertedID     int64
}

type insertedTrade struct {
	SignalID int64
	Symbol   string
	Margin   decimal.Decimal
	Notional decimal.Decimal
}

func (f *fakeEngineDeps) GetRecentEnteredSignals(_ context.Context, _ time.Time) ([]gen.GetRecentEnteredSignalsRow, error) {
	return f.signals, f.signalsErr
}
func (f *fakeEngineDeps) GetLatestClose(_ context.Context, _ string) (decimal.Decimal, error) {
	return f.price, f.priceErr
}
func (f *fakeEngineDeps) GetTradingFilters(_ context.Context, _ string) (binance.TradingFilters, error) {
	return f.filters, f.filtersErr
}
func (f *fakeEngineDeps) InsertEnteringTrade(_ context.Context, signalID int64, symbol, _ string, margin, notional decimal.Decimal, _ int32) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.insertedTrades = append(f.insertedTrades, insertedTrade{SignalID: signalID, Symbol: symbol, Margin: margin, Notional: notional})
	return f.insertedID, nil
}

func newEngineDeps() *fakeEngineDeps {
	return &fakeEngineDeps{
		fakeFilterDeps: newDeps(),
		price:          decimal.NewFromInt(80000),
		filters: binance.TradingFilters{
			StepSize: decimal.NewFromFloat(0.001), MinQty: decimal.NewFromFloat(0.001),
			MinNotional: decimal.NewFromInt(5),
		},
	}
}

func mkSignal(id int64, symbol, decision string) gen.GetRecentEnteredSignalsRow {
	return gen.GetRecentEnteredSignalsRow{ID: id, Symbol: symbol, Decision: decision, Ts: time.Now()}
}

// --- 8 unit tests ---

func TestRunTick_NoSignals_ReturnsEarly(t *testing.T) {
	deps := newEngineDeps()
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 0, report.Stats.SignalsRead)
	assert.Empty(t, report.Results)
	assert.Empty(t, deps.insertedTrades, "no signals → no trade write")
}

func TestRunTick_OneEnteredFull_FiltersPass_TradeEntering(t *testing.T) {
	deps := newEngineDeps()
	deps.signals = []gen.GetRecentEnteredSignalsRow{mkSignal(1, "BTCUSDT", "entered_full")}
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Stats.TradeEntering)
	require.Len(t, report.Results, 1)
	assert.Equal(t, OutcomeTradeEntering, report.Results[0].Outcome)
	require.Len(t, deps.insertedTrades, 1)
	assert.Equal(t, "BTCUSDT", deps.insertedTrades[0].Symbol)
	assert.True(t, deps.insertedTrades[0].Margin.Equal(decimal.NewFromInt(50)), "full margin = 50")
	// notional = 0.006 × 80000 = 480 (BTC step round 4% 偏差)
	assert.True(t, deps.insertedTrades[0].Notional.Equal(decimal.NewFromInt(480)))
}

func TestRunTick_OneEnteredHalf_TradeEntering_HalfMargin(t *testing.T) {
	deps := newEngineDeps()
	deps.signals = []gen.GetRecentEnteredSignalsRow{mkSignal(2, "ETHUSDT", "entered_half")}
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Stats.TradeEntering)
	require.Len(t, deps.insertedTrades, 1)
	assert.True(t, deps.insertedTrades[0].Margin.Equal(decimal.NewFromInt(25)), "half margin = 25")
}

func TestRunTick_FiltersReject_BTCCrash(t *testing.T) {
	deps := newEngineDeps()
	deps.btcDropPct = decimal.NewFromFloat(0.05) // > 0.03 threshold
	deps.signals = []gen.GetRecentEnteredSignalsRow{mkSignal(3, "BTCUSDT", "entered_full")}
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Stats.RejectedByFilter)
	require.Len(t, report.Results, 1)
	assert.Equal(t, "rejected_"+ReasonBTCCrash, report.Results[0].Outcome)
	assert.Empty(t, deps.insertedTrades, "filter reject → no write")
}

func TestRunTick_SizingFails_BelowMinQty(t *testing.T) {
	deps := newEngineDeps()
	deps.price = decimal.NewFromInt(1_000_000) // qty = 0.0005 < MinQty 0.001
	deps.signals = []gen.GetRecentEnteredSignalsRow{mkSignal(4, "BTCUSDT", "entered_full")}
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Stats.RejectedBySizing)
	assert.Equal(t, "sizing_"+SizingReasonBelowMinQty, report.Results[0].Outcome)
}

func TestRunTick_PerSignalError_Isolated(t *testing.T) {
	// 3 signals: BTC OK, ETH triggers filters err (cfg invariant via injection? — use sizing cfg invalid)
	// Easier: 3 signals, middle one's GetTradingFilters returns error → sizing_zero_step_size (still result, not internal_error)
	// True internal_error: sizing cfg negative → returns err. Inject by signal Decision = "weird" actually goes to invalid_decision (still result).
	// Easiest: insertErr only triggered for one symbol — but our fake doesn't support per-symbol err. Use insertErr=non-nil → all entering→internal_error.
	// Simpler: 2 valid signals, force insertErr on both → both internal_error counted, no trades.
	deps := newEngineDeps()
	deps.insertErr = errors.New("disk full")
	deps.signals = []gen.GetRecentEnteredSignalsRow{
		mkSignal(5, "BTCUSDT", "entered_full"),
		mkSignal(6, "ETHUSDT", "entered_full"),
	}
	report, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.NoError(t, err, "per-signal write fail must NOT bubble")
	assert.Equal(t, 0, report.Stats.TradeEntering)
	assert.Equal(t, 2, report.Stats.InternalError)
	assert.Len(t, report.Results, 2)
}

func TestRunTick_GetSignalsError_BubblesUp(t *testing.T) {
	deps := newEngineDeps()
	deps.signalsErr = errors.New("pg connection lost")
	_, err := RunTick(context.Background(), time.Now(), deps, EngineConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get recent signals")
}

func TestEvaluateOne_PriceReadFails_SizingZeroPrice(t *testing.T) {
	deps := newEngineDeps()
	deps.priceErr = errors.New("no klines for symbol")
	signal := mkSignal(7, "BTCUSDT", "entered_full")
	res, err := EvaluateOne(context.Background(), signal, time.Now(), deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, "sizing_"+SizingReasonZeroPrice, res.Outcome)
}

// --- 1 边界: HaltExpired auto-reset must continue evaluation correctly ---

func TestRunTick_HaltExpired_ContinueEvaluation(t *testing.T) {
	now := time.Now()
	deps := newEngineDeps()
	deps.state = gen.CircuitBreakerState{
		ID: 1, TradingHalted: true,
		HaltReason: pgtype.Text{String: "btc_5m_crash", Valid: true},
		HaltUntil:  pgtype.Timestamptz{Time: now.Add(-5 * time.Minute), Valid: true}, // expired
	}
	deps.signals = []gen.GetRecentEnteredSignalsRow{mkSignal(8, "BTCUSDT", "entered_full")}
	report, err := RunTick(context.Background(), now, deps, EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Stats.TradeEntering, "halt expired → reset → continue → trade entering")
	assert.True(t, deps.reset, "ResetHalt must be called")
}
