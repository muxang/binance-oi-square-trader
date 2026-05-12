package execution

import (
	"context"
	"errors"
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

// fakePositionDeps records DB calls + supplies canned responses.
type fakePositionDeps struct {
	openTrades            []gen.ListOpenTradesForSyncRow
	openTradesErr         error
	syncUpdates           []gen.UpdatePositionStateSyncParams
	tradesFailed          []gen.UpdateTradeFailedParams
	exitsInserted         []gen.InsertTradeExitParams
	rcaInserted           []gen.InsertHaltRCAParams
	haltsTripped          []gen.TripGenericHaltParams
	positionStatesDeleted []int64 // v0.2 Catch 5
}

func (f *fakePositionDeps) ListOpenTradesForSync(_ context.Context) ([]gen.ListOpenTradesForSyncRow, error) {
	return f.openTrades, f.openTradesErr
}
func (f *fakePositionDeps) UpdatePositionStateSync(_ context.Context, arg gen.UpdatePositionStateSyncParams) error {
	f.syncUpdates = append(f.syncUpdates, arg)
	return nil
}
func (f *fakePositionDeps) UpdateTradeFailed(_ context.Context, arg gen.UpdateTradeFailedParams) error {
	f.tradesFailed = append(f.tradesFailed, arg)
	return nil
}
func (f *fakePositionDeps) InsertTradeExit(_ context.Context, arg gen.InsertTradeExitParams) error {
	f.exitsInserted = append(f.exitsInserted, arg)
	return nil
}
func (f *fakePositionDeps) DeletePositionState(_ context.Context, tradeID int64) error {
	f.positionStatesDeleted = append(f.positionStatesDeleted, tradeID)
	return nil
}
func (f *fakePositionDeps) InsertHaltRCA(_ context.Context, arg gen.InsertHaltRCAParams) (gen.InsertHaltRCARow, error) {
	f.rcaInserted = append(f.rcaInserted, arg)
	return gen.InsertHaltRCARow{ID: int64(len(f.rcaInserted)), TriggeredAt: time.Now()}, nil
}
func (f *fakePositionDeps) TripGenericHalt(_ context.Context, arg gen.TripGenericHaltParams) error {
	f.haltsTripped = append(f.haltsTripped, arg)
	return nil
}

type fakeBinance struct {
	positions    []binance.PositionRisk
	positionsErr error
	sellCalls    []string // record symbol+side+qty for assertion
}

func (f *fakeBinance) GetPositionRisk(_ context.Context, _ string) ([]binance.PositionRisk, error) {
	return f.positions, f.positionsErr
}
func (f *fakeBinance) PlaceMarketOrder(_ context.Context, symbol, side, qty, _ string) (binance.OrderResult, error) {
	f.sellCalls = append(f.sellCalls, symbol+":"+side+":"+qty)
	return binance.OrderResult{
		OrderID:     999, Symbol: symbol, Status: "FILLED",
		AvgPrice:    decimal.NewFromFloat(2000),
		ExecutedQty: decimal.RequireFromString(qty),
	}, nil
}

func newTestPM(t *testing.T, fdeps *fakePositionDeps, fbc *fakeBinance) *PositionManager {
	t.Helper()
	rdb, _ := redis.ParseURL("redis://127.0.0.1:6379/15") // unused if no zset rebuild
	client := redis.NewClient(rdb)
	pm := NewPositionManager(fdeps, fbc, client, zerolog.Nop())
	pm.nowFn = func() time.Time { return time.Unix(1778500000, 0).UTC() }
	return pm
}

func TestSyncTick_EmptyTrades_NoBinanceCall(t *testing.T) {
	fdeps := &fakePositionDeps{openTrades: nil}
	fbc := &fakeBinance{}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Empty(t, fdeps.syncUpdates, "no trades → no sync")
}

func TestSyncTick_OpenTrades_GetPositionRiskError(t *testing.T) {
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 1, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50)},
	}}
	fbc := &fakeBinance{positionsErr: errors.New("network")}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Empty(t, fdeps.syncUpdates, "binance error → no DB write")
}

func TestSyncTick_HealthyPosition_UpdatesStateAndComputesMarginRatio(t *testing.T) {
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 10, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "BTCUSDT", PositionAmt: decimal.NewFromFloat(0.006), MarkPrice: decimal.NewFromInt(81000),
			UnrealizedProfit: decimal.NewFromInt(1)}, // +1 USDT profit
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Len(t, fdeps.syncUpdates, 1, "1 trade → 1 state update")
	assert.Equal(t, int64(10), fdeps.syncUpdates[0].TradeID)
	assert.True(t, fdeps.syncUpdates[0].CurrentQty.Equal(decimal.NewFromFloat(0.006)))
	assert.Empty(t, fbc.sellCalls, "margin_ratio < 0.8 → no margin call")
	assert.Empty(t, fdeps.exitsInserted)
}

func TestSyncTick_MarginCall_TriggersEmergencyExit(t *testing.T) {
	// margin=50, unrealized_pnl=-45 → margin_ratio = 45/50 = 0.9 > 0.8 → trigger.
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 11, Symbol: "ETHUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "ETHUSDT", PositionAmt: decimal.NewFromFloat(0.5), MarkPrice: decimal.NewFromInt(2000),
			UnrealizedProfit: decimal.NewFromInt(-45)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Len(t, fbc.sellCalls, 1, "margin_ratio 0.9 > 0.8 → 1 SELL")
	assert.Contains(t, fbc.sellCalls[0], "ETHUSDT:SELL:0.5")
	assert.Len(t, fdeps.exitsInserted, 1, "trade_exits row")
	assert.Equal(t, "margin_call", fdeps.exitsInserted[0].Type)
	assert.Len(t, fdeps.tradesFailed, 1, "trade marked failed")
}

func TestSyncTick_DirectionMismatch_LogsDriftNoBlock(t *testing.T) {
	// DB says LONG but binance shows SHORT (negative positionAmt).
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 12, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "BTCUSDT", PositionAmt: decimal.NewFromFloat(-0.006), MarkPrice: decimal.NewFromInt(81000),
			UnrealizedProfit: decimal.NewFromInt(1)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	// Drift logged via metric, but state still updates (Round 3 v0.1: log, no halt).
	assert.Len(t, fdeps.syncUpdates, 1, "drift logs but state still syncs")
}

func TestSyncTick_MissingPosition_LogsDriftSkipsState(t *testing.T) {
	// DB has open trade for ARBUSDT but binance returns no position for it.
	// Round 4: this is local_only_orphan → trip halt + write RCA.
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 13, Symbol: "ARBUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "ETHUSDT", PositionAmt: decimal.NewFromFloat(0.5)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Empty(t, fdeps.syncUpdates, "missing position → no state update")
	// Round 4 reconcile: ARBUSDT local-only triggers halt + rca; ETHUSDT
	// binance-only ALSO triggers halt + rca → 2 each.
	assert.Len(t, fdeps.rcaInserted, 2, "1 local-only + 1 binance-only RCA")
	assert.Len(t, fdeps.haltsTripped, 2, "halt tripped for each event")
	assert.Equal(t, "local_only_orphan", fdeps.rcaInserted[0].HaltType)
	assert.Equal(t, "binance_only_unknown", fdeps.rcaInserted[1].HaltType)
}

func TestSyncTick_QtyDriftOver5pct_TripsHalt(t *testing.T) {
	// DB current_qty = 0.006, binance positionAmt = 0.0048 → 20% drift > 5% threshold.
	qty := pgtype.Numeric{}
	_ = qty.Scan("0.006")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 20, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			CurrentQty: qty},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "BTCUSDT", PositionAmt: decimal.NewFromFloat(0.0048), MarkPrice: decimal.NewFromInt(80000),
			UnrealizedProfit: decimal.NewFromInt(1)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Len(t, fdeps.rcaInserted, 1, "1 drift_exceeded RCA")
	assert.Equal(t, "drift_exceeded", fdeps.rcaInserted[0].HaltType)
	assert.Len(t, fdeps.haltsTripped, 1)
}

func TestSyncTick_QtyDriftBelow5pct_NoHalt(t *testing.T) {
	// DB 0.006, binance 0.00594 → 1% drift, < 5% threshold → log only.
	qty := pgtype.Numeric{}
	_ = qty.Scan("0.006")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 21, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			CurrentQty: qty},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "BTCUSDT", PositionAmt: decimal.NewFromFloat(0.00594), MarkPrice: decimal.NewFromInt(80000),
			UnrealizedProfit: decimal.NewFromInt(1)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Empty(t, fdeps.rcaInserted, "1% drift below 5% threshold → no RCA")
	assert.Empty(t, fdeps.haltsTripped, "no halt")
}

// v0.2 Catch 2: orphan branch must DeleteLabelValues so the gauge doesn't
// keep the last-tick value for hours/days during halt.
func TestSyncTick_LocalOnlyOrphan_ClearsMarginRatioGauge(t *testing.T) {
	const sym = "ORPHANUSDT"
	// Seed gauge with a non-zero value from a prior tick.
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.42)
	require.InDelta(t, 0.42, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 49, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	// Binance returns no position for ORPHANUSDT → local_only_orphan.
	fbc := &fakeBinance{positions: nil}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())

	// orphan branch ran → gauge cleared. Re-reading WithLabelValues recreates
	// it at zero (DeleteLabelValues removed the prior 0.42 series).
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"orphan branch must DeleteLabelValues to avoid stale gauge")
	assert.Len(t, fdeps.rcaInserted, 1)
	assert.Equal(t, "local_only_orphan", fdeps.rcaInserted[0].HaltType)
}

// v0.2 Step 5 race-window defense: when local_only_orphan is detected AND
// the trade has an Algo + reconciler is wired, position_manager must consult
// algo_reconciler before tripping halt. If Algo is FINISHED, auto-close and
// skip the halt entirely. Eliminates the cron-ordering race where
// position_manager queries DB before algo_polling collector reconciles.
func TestSyncTick_LocalOnlyOrphan_ReconcilesAlgoBeforeHalt(t *testing.T) {
	const sym = "RACEUSDT"
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.20)

	// Trade row carries algo_id "77777" — defense will consult reconciler.
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("5.0")
	qty := pgtype.Numeric{}
	_ = qty.Scan("100")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 200, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "77777", Valid: true},
			CurrentQty:                 qty,
		},
	}}
	fbc := &fakeBinance{positions: nil} // orphan situation: no Binance position
	pm := newTestPM(t, fdeps, fbc)

	// Real AlgoReconciler with fake deps — answers FINISHED for algo 77777.
	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		77777: {
			AlgoID: 77777, Symbol: sym, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(4.7), // -6% drop, disaster triggered
			Quantity:    decimal.NewFromFloat(100),
		},
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	ar.nowFn = func() time.Time { return time.Unix(1778500000, 0).UTC() }
	pm.SetAlgoReconciler(ar)

	pm.SyncTick(context.Background())

	// Defense fired → no orphan halt.
	assert.Empty(t, fdeps.rcaInserted, "race-window defense skipped orphan RCA")
	assert.Empty(t, fdeps.haltsTripped, "race-window defense skipped halt")

	// AlgoReconciler.autoClose path executed on algoDeps.
	require.Len(t, algoDeps.exits, 1, "auto-close inserted trade_exits")
	assert.Equal(t, ExitReasonDisaster, algoDeps.exits[0].Type)
	require.Len(t, algoDeps.closed, 1, "auto-close updated trade")
	require.Len(t, algoDeps.stateDeleted, 1, "auto-close DELETE position_state")
	assert.Equal(t, int64(200), algoDeps.stateDeleted[0])

	// margin_ratio gauge cleared (idempotent — both autoClose and orphan
	// path delete; this verifies the final state).
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)
}

// v0.2 Step 5 reverse: if Algo is NOT finished, defense returns false and
// orphan halt still fires (existing Round 4 behavior preserved).
func TestSyncTick_LocalOnlyOrphan_NoAlgoFinish_FallsBackToHalt(t *testing.T) {
	const sym = "STILLORPHAN"
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 201, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			BinanceDisasterStopOrderID: pgtype.Text{String: "88888", Valid: true},
		},
	}}
	fbc := &fakeBinance{positions: nil}
	pm := newTestPM(t, fdeps, fbc)

	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		88888: {AlgoID: 88888, AlgoStatus: "WORKING"}, // not finished
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	pm.SetAlgoReconciler(ar)

	pm.SyncTick(context.Background())
	assert.Empty(t, algoDeps.exits, "Algo not FINISHED → no auto-close")
	require.Len(t, fdeps.rcaInserted, 1, "defense returned false → orphan halt fires")
	assert.Equal(t, "local_only_orphan", fdeps.rcaInserted[0].HaltType)
}

// v0.2 Catch 5 + Catch 2: emergencyExit must mirror persistClose terminal
// cleanup (DeletePositionState + ZREM + DeleteLabelValues).
func TestSyncTick_MarginCall_EmergencyExitCleansAllState(t *testing.T) {
	const sym = "EMERUSDT"
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.95)
	require.InDelta(t, 0.95, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 99, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	// Unrealized -45 / margin 50 = 0.9 > 0.8 → margin_call → emergencyExit.
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: sym, PositionAmt: decimal.NewFromFloat(0.5), MarkPrice: decimal.NewFromInt(2000),
			UnrealizedProfit: decimal.NewFromInt(-45)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())

	require.Len(t, fbc.sellCalls, 1, "margin_call → 1 emergency SELL")
	require.Len(t, fdeps.tradesFailed, 1, "trade marked failed")
	require.Len(t, fdeps.positionStatesDeleted, 1, "position_states row DELETEd")
	assert.Equal(t, int64(99), fdeps.positionStatesDeleted[0])
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"emergencyExit must DeleteLabelValues to avoid stale gauge")
}

func TestSyncTick_BinanceOnlyUnknown_TripsHalt(t *testing.T) {
	// DB empty (no open trades) but binance reports a position → binance_only_unknown.
	// (When DB is empty, code short-circuits BEFORE checking binance for unknowns.
	// To exercise binance_only_unknown path, DB must have OTHER open trades for
	// localSymbols map to be initialized.)
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 30, Symbol: "BTCUSDT", Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: "BTCUSDT", PositionAmt: decimal.NewFromFloat(0.006), MarkPrice: decimal.NewFromInt(80000)},
		{Symbol: "ARBUSDT", PositionAmt: decimal.NewFromFloat(100), MarkPrice: decimal.NewFromFloat(1.5)},
	}}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())
	assert.Len(t, fdeps.rcaInserted, 1, "ARBUSDT binance-only → 1 RCA")
	assert.Equal(t, "binance_only_unknown", fdeps.rcaInserted[0].HaltType)
}
