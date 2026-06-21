package execution

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/storage/postgres/gen"
)

// fakeExitDeps satisfies ExitManagerDeps.
type fakeExitDeps struct {
	openTrades        []gen.ListOpenTradesForExitRow
	openTradesErr     error
	closingMarked     []int64
	closed            []gen.UpdateTradeClosedParams
	failed            []gen.UpdateTradeFailedParams
	exits             []gen.InsertTradeExitParams
	stateDeleted      []int64
	cbUpdated         []gen.UpdateAfterTradeCloseParams
	rcaInserted       []gen.InsertHaltRCAParams
	haltsTripped      []gen.TripGenericHaltParams
}

func (f *fakeExitDeps) ListOpenTradesForExit(_ context.Context) ([]gen.ListOpenTradesForExitRow, error) {
	return f.openTrades, f.openTradesErr
}
func (f *fakeExitDeps) UpdateTradeClosing(_ context.Context, id int64) error {
	f.closingMarked = append(f.closingMarked, id)
	return nil
}
func (f *fakeExitDeps) UpdateTradeClosed(_ context.Context, arg gen.UpdateTradeClosedParams) error {
	f.closed = append(f.closed, arg)
	return nil
}
func (f *fakeExitDeps) UpdateTradeFailed(_ context.Context, arg gen.UpdateTradeFailedParams) error {
	f.failed = append(f.failed, arg)
	return nil
}
func (f *fakeExitDeps) InsertTradeExit(_ context.Context, arg gen.InsertTradeExitParams) error {
	f.exits = append(f.exits, arg)
	return nil
}
func (f *fakeExitDeps) DeletePositionState(_ context.Context, id int64) error {
	f.stateDeleted = append(f.stateDeleted, id)
	return nil
}
func (f *fakeExitDeps) UpdateAfterTradeClose(_ context.Context, arg gen.UpdateAfterTradeCloseParams) error {
	f.cbUpdated = append(f.cbUpdated, arg)
	return nil
}
func (f *fakeExitDeps) InsertHaltRCA(_ context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error) {
	f.rcaInserted = append(f.rcaInserted, arg)
	return gen.InsertHaltRCARow{ID: int64(len(f.rcaInserted))}, nil
}
func (f *fakeExitDeps) TripGenericHalt(_ context.Context, arg gen.TripGenericHaltParams) error {
	f.haltsTripped = append(f.haltsTripped, arg)
	return nil
}

// fakeExitBinance satisfies ExitBinanceClient.
type fakeExitBinance struct {
	cancelCalls []int64
	cancelErr   error
	sellCalls   []string // "symbol:qty"
	sellResp    binance.OrderResult
	sellErr     error
}

func (f *fakeExitBinance) CancelAlgoOrder(_ context.Context, _ string, algoID int64) error {
	f.cancelCalls = append(f.cancelCalls, algoID)
	return f.cancelErr
}
func (f *fakeExitBinance) PlaceMarketOrder(_ context.Context, symbol, _, qty, _ string) (binance.OrderResult, error) {
	f.sellCalls = append(f.sellCalls, symbol+":"+qty)
	if f.sellErr != nil {
		return binance.OrderResult{}, f.sellErr
	}
	return f.sellResp, nil
}

// R.33: exit path uses the reduceOnly variant; share the same recording so
// existing tests keep validating the SELL call count + qty.
func (f *fakeExitBinance) PlaceMarketOrderReduceOnly(ctx context.Context, symbol, side, qty, cid string) (binance.OrderResult, error) {
	return f.PlaceMarketOrder(ctx, symbol, side, qty, cid)
}
func (f *fakeExitBinance) GetOrderByClientID(_ context.Context, _, _ string) (binance.OrderResult, error) {
	return binance.OrderResult{}, nil
}

func newTestExit(t *testing.T) (*ExitManager, *fakeExitDeps, *fakeExitBinance) {
	t.Helper()
	fdeps := &fakeExitDeps{}
	fbc := &fakeExitBinance{
		sellResp: binance.OrderResult{
			OrderID: 999, Status: "FILLED",
			AvgPrice:    decimal.NewFromFloat(81000),
			ExecutedQty: decimal.NewFromFloat(0.006),
		},
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"}) // unreachable; ZRem fails silently
	em := NewExitManager(fdeps, fbc, rdb, zerolog.Nop())
	em.nowFn = func() time.Time { return time.Unix(1778520000, 0).UTC() } // fixed
	return em, fdeps, fbc
}

func mkOpenTrade(id int64, symbol string, entryHoursAgo float64, entryPrice float64, qty float64, algoID string) gen.ListOpenTradesForExitRow {
	now := time.Unix(1778520000, 0).UTC()
	entryTs := now.Add(-time.Duration(entryHoursAgo * float64(time.Hour)))
	ep := pgtype.Numeric{}
	_ = ep.Scan(decimal.NewFromFloat(entryPrice).String())
	q := pgtype.Numeric{}
	_ = q.Scan(decimal.NewFromFloat(qty).String())
	algoText := pgtype.Text{Valid: false}
	if algoID != "" {
		algoText = pgtype.Text{String: algoID, Valid: true}
	}
	return gen.ListOpenTradesForExitRow{
		ID:                         id,
		Symbol:                     symbol,
		Direction:                  "LONG",
		EntryTs:                    pgtype.Timestamptz{Time: entryTs, Valid: true},
		EntryPrice:                 ep,
		Margin:                     decimal.NewFromInt(50),
		Leverage:                   10,
		BinanceDisasterStopOrderID: algoText,
		CurrentQty:                 q,
	}
}

// --- 5 unit tests ---

func TestEvaluateTick_EmptyTrades_NoOp(t *testing.T) {
	em, fdeps, fbc := newTestExit(t)
	em.EvaluateTick(context.Background())
	assert.Empty(t, fdeps.closed)
	assert.Empty(t, fbc.sellCalls)
}

func TestEvaluateTick_HardTimeout_AlwaysCloses(t *testing.T) {
	em, fdeps, fbc := newTestExit(t)
	// Entry 73 hours ago → hard timeout (>= 72h).
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(1, "BTCUSDT", 73, 80000, 0.006, "1000")}
	em.EvaluateTick(context.Background())
	require.Len(t, fbc.cancelCalls, 1, "cancel algo before SELL")
	assert.Equal(t, int64(1000), fbc.cancelCalls[0])
	require.Len(t, fbc.sellCalls, 1)
	assert.Contains(t, fbc.sellCalls[0], "BTCUSDT:0.006")
	require.Len(t, fdeps.closed, 1)
	assert.Equal(t, "hard_timeout", fdeps.closed[0].ExitReason.String)
	require.Len(t, fdeps.exits, 1)
	assert.Equal(t, "hard_timeout", fdeps.exits[0].Type)
	require.Len(t, fdeps.cbUpdated, 1)
}

func TestEvaluateTick_SoftTimeout_ClosesOnlyWhenUnderwater(t *testing.T) {
	// 25 hours hold, no Redis price → unrealized=0 → NOT < 0 → skip.
	em, fdeps, fbc := newTestExit(t)
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(2, "BTCUSDT", 25, 80000, 0.006, "")}
	em.EvaluateTick(context.Background())
	assert.Empty(t, fbc.sellCalls, "soft_timeout requires unrealized<0, zero default does not qualify")
	assert.Empty(t, fdeps.closed)
}

func TestEvaluateTick_BelowSoftThreshold_NoExit(t *testing.T) {
	// 23 hours hold → below soft (24h).
	em, fdeps, fbc := newTestExit(t)
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(3, "ETHUSDT", 23, 2000, 0.5, "")}
	em.EvaluateTick(context.Background())
	assert.Empty(t, fbc.sellCalls)
	assert.Empty(t, fdeps.closed)
}

func TestEvaluateTick_SellFailure_TripsHaltAndKeepsClosing(t *testing.T) {
	em, fdeps, fbc := newTestExit(t)
	fbc.sellErr = assert.AnError
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(4, "BTCUSDT", 100, 80000, 0.006, "")}
	em.EvaluateTick(context.Background())
	assert.Len(t, fbc.sellCalls, 1, "SELL attempted")
	assert.Empty(t, fdeps.closed, "no terminal UpdateTradeClosed on failure")
	assert.Len(t, fdeps.closingMarked, 1, "trade went to 'closing' state")
	assert.Len(t, fdeps.rcaInserted, 1, "halt RCA written")
	assert.Equal(t, "close_failed", fdeps.rcaInserted[0].HaltType)
	assert.Len(t, fdeps.haltsTripped, 1, "halt tripped")
}

// v0.2 Catch 2: persistClose must DeleteLabelValues so margin_ratio gauge
// doesn't keep the last in-flight value after a clean close.
func TestEvaluateTick_HardTimeout_ClearsMarginRatioGauge(t *testing.T) {
	const sym = "PCLOSEUSDT"
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.31)
	require.InDelta(t, 0.31, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	em, fdeps, _ := newTestExit(t)
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(50, sym, 73, 80000, 0.006, "")}
	em.EvaluateTick(context.Background())
	require.Len(t, fdeps.closed, 1, "hard_timeout closes the trade")
	require.Len(t, fdeps.stateDeleted, 1, "position_states deleted")
	assert.Equal(t, int64(50), fdeps.stateDeleted[0])
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"persistClose must DeleteLabelValues on the symbol gauge")
}

// v0.2 Catch 5 + Catch 2: markCloseFailed (terminal current_qty=0 path) must
// mirror persistClose teardown — DELETE position_states + clear gauge.
func TestEvaluateTick_ZeroQty_MarkCloseFailedCleansState(t *testing.T) {
	const sym = "ZEROQTYUSDT"
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.77)
	require.InDelta(t, 0.77, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	em, fdeps, fbc := newTestExit(t)
	// 73h hold + qty=0 → hard_timeout path enters closePosition, hits qty=0
	// branch → markCloseFailed.
	fdeps.openTrades = []gen.ListOpenTradesForExitRow{mkOpenTrade(51, sym, 73, 80000, 0, "")}
	em.EvaluateTick(context.Background())
	assert.Empty(t, fbc.sellCalls, "qty=0 → no SELL attempt")
	require.Len(t, fdeps.failed, 1, "trade marked failed via markCloseFailed")
	assert.Equal(t, "current_qty_zero", fdeps.failed[0].ExitReason.String)
	require.Len(t, fdeps.stateDeleted, 1, "position_states cleaned on terminal failure")
	assert.Equal(t, int64(51), fdeps.stateDeleted[0])
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"markCloseFailed must clear margin_ratio gauge")
}
