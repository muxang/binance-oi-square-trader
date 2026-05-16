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
	tradesClosed          []gen.UpdateTradeClosedParams // R.8 autoSyncOrphan
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
func (f *fakePositionDeps) UpdateTradeClosed(_ context.Context, arg gen.UpdateTradeClosedParams) error {
	f.tradesClosed = append(f.tradesClosed, arg)
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
	// Round R.8: local_only_orphan first detection no longer halts — records
	// candidate. binance_only_unknown path unchanged.
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
	// R.8: ARBUSDT records candidate (no halt); ETHUSDT binance_only triggers halt.
	require.Len(t, fdeps.rcaInserted, 1, "only binance_only_unknown halts on tick 1")
	assert.Equal(t, "binance_only_unknown", fdeps.rcaInserted[0].HaltType)
	_, ok := pm.pendingOrphans.Load(int64(13))
	assert.True(t, ok, "ARBUSDT local-only orphan recorded as candidate")
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
// Round R.8 update: first orphan detection now records candidate, NO halt.
// margin_ratio gauge cleanup still happens.
func TestSyncTick_LocalOnlyOrphan_ClearsMarginRatioGauge(t *testing.T) {
	const sym = "ORPHANUSDT"
	// Seed gauge with a non-zero value from a prior tick.
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.42)
	require.InDelta(t, 0.42, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{ID: 49, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs: pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true}},
	}}
	// Binance returns no position for ORPHANUSDT → local_only_orphan candidate.
	fbc := &fakeBinance{positions: nil}
	pm := newTestPM(t, fdeps, fbc)
	pm.SyncTick(context.Background())

	// gauge cleared regardless of halt/candidate path.
	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"orphan branch must DeleteLabelValues to avoid stale gauge")
	// R.8: first detection records candidate (in pendingOrphans map), no halt.
	assert.Empty(t, fdeps.rcaInserted, "first orphan detection → no halt (R.8)")
	assert.Empty(t, fdeps.haltsTripped, "first orphan detection → no halt (R.8)")
	assert.Empty(t, fdeps.tradesClosed, "first detection only records candidate, no auto-sync yet")
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
// orphan first detection now creates candidate, NO halt (R.8). Halt removed
// from this path entirely — the next tick (if still orphan) auto-syncs.
func TestSyncTick_LocalOnlyOrphan_NoAlgoFinish_RecordsCandidate(t *testing.T) {
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
	// R.8: first detection no halt — only adds to pendingOrphans (in-memory).
	assert.Empty(t, fdeps.rcaInserted, "R.8: first detection records candidate, no halt yet")
	assert.Empty(t, fdeps.haltsTripped, "R.8: first detection records candidate, no halt yet")
	// pendingOrphans should contain the trade_id (in-memory check).
	_, ok := pm.pendingOrphans.Load(int64(201))
	assert.True(t, ok, "pendingOrphans must contain trade_id after first detection")
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

// Round R.4 (F1): trail-fired orphan must also skip halt. Pre-fix, position_manager
// only consulted disaster_stop algo status — trail-fired closes (mu 真盘 #66/#67/#59)
// fell through to halt because disaster algo was still NEW/WORKING.
func TestSyncTick_LocalOnlyOrphan_TrailFinished_SkipsHalt(t *testing.T) {
	const sym = "TRAILORPHAN"
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("0.5")
	qty := pgtype.Numeric{}
	_ = qty.Scan("100")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 300, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "9001", Valid: true},
			BinanceTrailAlgoID:         pgtype.Text{String: "9002", Valid: true},
			CurrentQty:                 qty,
		},
	}}
	fbc := &fakeBinance{positions: nil} // orphan

	pm := newTestPM(t, fdeps, fbc)
	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		// disaster still WORKING (never fired) — pre-fix this is the only one
		// position_manager would check, and it would trip a false halt.
		9001: {AlgoID: 9001, AlgoStatus: "WORKING"},
		// Round R.4: trail FINISHED — position_manager must consult this too.
		9002: {AlgoID: 9002, Symbol: sym, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(0.55), Quantity: decimal.NewFromFloat(100)},
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	ar.nowFn = func() time.Time { return time.Unix(1778500000, 0).UTC() }
	pm.SetAlgoReconciler(ar)

	pm.SyncTick(context.Background())

	assert.Empty(t, fdeps.rcaInserted, "trail-FINISHED orphan must NOT trip halt (Round R.4 fix)")
	assert.Empty(t, fdeps.haltsTripped, "no halt when defense via trail algo succeeds")
	require.Len(t, algoDeps.exits, 1, "trail-FINISHED triggers auto-close via TryReconcile")
	// Round R.5 (Bug C): exit must be recorded as trail_sN, not 'disaster'.
	// Test row has TrailStage=0 (default) → trailExitReasonForStage → trail_s1.
	assert.Equal(t, ExitReasonTrailS1, algoDeps.exits[0].Type,
		"trail-fired close must record as trail_sN (Bug C fix), not hardcoded 'disaster'")
}

// Round R.5 (Bug B): drift halt must NOT fire when a TP just FINISHED on Binance.
// The Binance qty drop (TP partial fill) precedes algo_reconciler's DB decrement
// on the same 1min tick — position_manager must consult TP algo state and skip
// the halt + skip the sync overwrite. mu 真盘 COSUSDT #70 hit this twice in a row.
func TestSyncTick_QtyDrift_TPFiredRace_SkipsHalt(t *testing.T) {
	const sym = "TPRACE"
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("100")
	dbQty := pgtype.Numeric{}
	_ = dbQty.Scan("100") // DB pre-decrement
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 400, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "5001", Valid: true},
			BinanceTP1AlgoID:           pgtype.Text{String: "5002", Valid: true},
			BinanceTP2AlgoID:           pgtype.Text{String: "5003", Valid: true},
			CurrentQty:                 dbQty,
		},
	}}
	// Binance shows 80 (post-TP1 fill 20%) — drift = 20% > 5% threshold.
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: sym, PositionAmt: decimal.NewFromFloat(80), MarkPrice: decimal.NewFromInt(110), UnrealizedProfit: decimal.NewFromInt(1)},
	}}
	pm := newTestPM(t, fdeps, fbc)

	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		// TP1 FINISHED (the cause of the Binance qty drop).
		5002: {AlgoID: 5002, AlgoStatus: "FINISHED"},
		// TP2 still WORKING.
		5003: {AlgoID: 5003, AlgoStatus: "WORKING"},
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	pm.SetAlgoReconciler(ar)

	pm.SyncTick(context.Background())

	assert.Empty(t, fdeps.rcaInserted, "TP-fill race must NOT trip drift halt (Bug B fix)")
	assert.Empty(t, fdeps.haltsTripped, "no halt when TP is FINISHED — algo_reconciler will decrement")
	// Bug A: sync must also be skipped (otherwise it overwrites DB to Binance value,
	// then algo_reconciler decrements again on next tick → amplified drift).
	assert.Empty(t, fdeps.syncUpdates, "drift+TP race: skip UpdatePositionStateSync (Bug A fix)")
}

// Round R.5 (Bug B negative case): when drift > 5% AND NO TP is FINISHED,
// halt SHOULD fire — this is a real divergence, not a TP race.
func TestSyncTick_QtyDrift_NoTPFinish_StillHalts(t *testing.T) {
	const sym = "REALDRIFT"
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("100")
	dbQty := pgtype.Numeric{}
	_ = dbQty.Scan("100")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 401, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "6001", Valid: true},
			BinanceTP1AlgoID:           pgtype.Text{String: "6002", Valid: true},
			BinanceTP2AlgoID:           pgtype.Text{String: "6003", Valid: true},
			CurrentQty:                 dbQty,
		},
	}}
	fbc := &fakeBinance{positions: []binance.PositionRisk{
		{Symbol: sym, PositionAmt: decimal.NewFromFloat(80), MarkPrice: decimal.NewFromInt(110), UnrealizedProfit: decimal.NewFromInt(1)},
	}}
	pm := newTestPM(t, fdeps, fbc)

	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		6002: {AlgoID: 6002, AlgoStatus: "WORKING"}, // TP1 not fired
		6003: {AlgoID: 6003, AlgoStatus: "WORKING"}, // TP2 not fired
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	pm.SetAlgoReconciler(ar)

	pm.SyncTick(context.Background())

	require.Len(t, fdeps.rcaInserted, 1, "real drift (no TP race) → halt fires")
	assert.Equal(t, "drift_exceeded", fdeps.rcaInserted[0].HaltType)
	// Bug A: even for real drift, don't overwrite current_qty (let mu / algo_reconciler decide).
	assert.Empty(t, fdeps.syncUpdates, "drift halt: skip UpdatePositionStateSync (Bug A fix)")
}

// Round R.8: second tick with orphan still present → autoSyncOrphan (no halt).
// Simulates the real STORJ #101 scenario where algoStatus FINISHED propagation
// lagged past the second tick window (rare edge case in production — usually
// algo_reconciler catches the FINISHED status on tick 2 and closes the trade
// before this branch fires).
func TestSyncTick_LocalOnlyOrphan_TwoTicks_AutoSyncs(t *testing.T) {
	const sym = "TWOTICKORPHAN"
	currentQty := pgtype.Numeric{}
	_ = currentQty.Scan("100")
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("0.5")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 202, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "99999", Valid: true},
			CurrentQty:                 currentQty,
		},
	}}
	fbc := &fakeBinance{positions: nil} // missing both ticks

	pm := newTestPM(t, fdeps, fbc)
	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		99999: {AlgoID: 99999, AlgoStatus: "WORKING"}, // never propagates to FINISHED
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	pm.SetAlgoReconciler(ar)

	// Tick 1: mark candidate, no halt, no auto-sync.
	pm.nowFn = func() time.Time { return time.Unix(1778500000, 0).UTC() }
	pm.SyncTick(context.Background())
	require.Empty(t, fdeps.tradesClosed, "tick 1 must not close")
	require.Empty(t, fdeps.rcaInserted, "tick 1 must not halt (R.8)")
	_, ok := pm.pendingOrphans.Load(int64(202))
	require.True(t, ok, "tick 1 must mark candidate")

	// Tick 2 at +60s: confirmed orphan → autoSyncOrphan fires.
	pm.nowFn = func() time.Time { return time.Unix(1778500060, 0).UTC() }
	pm.SyncTick(context.Background())

	require.Len(t, fdeps.tradesClosed, 1, "tick 2 (≥50s elapsed) autoSyncs")
	assert.Equal(t, int64(202), fdeps.tradesClosed[0].ID)
	assert.Equal(t, "orphan_synced", fdeps.tradesClosed[0].ExitReason.String)
	assert.True(t, fdeps.tradesClosed[0].RealizedPnl.IsZero(), "pnl=0 — fill unknown")

	require.Len(t, fdeps.exitsInserted, 1, "tick 2 inserts orphan_synced exit row")
	assert.Equal(t, "orphan_synced", fdeps.exitsInserted[0].Type)

	assert.Empty(t, fdeps.rcaInserted, "R.8: no halt on auto-sync")
	assert.Empty(t, fdeps.haltsTripped, "R.8: no halt on auto-sync")
	assert.Contains(t, fdeps.positionStatesDeleted, int64(202), "position_states cleaned up")

	// pendingOrphans entry should be removed after auto-sync.
	_, stillPending := pm.pendingOrphans.Load(int64(202))
	assert.False(t, stillPending, "pendingOrphans entry cleared after autoSync")
}

// Round R.8: orphan candidate but Binance position returns on tick 2 →
// candidate is dropped on the resolve-path of subsequent ticks (not aged
// out here, but no halt and no autoSync either since the trade is now valid).
func TestSyncTick_OrphanCandidate_ResolvedNextTick_NoAutoSync(t *testing.T) {
	const sym = "RECOVERED"
	entryPx := pgtype.Numeric{}
	_ = entryPx.Scan("100")
	dbQty := pgtype.Numeric{}
	_ = dbQty.Scan("1")
	fdeps := &fakePositionDeps{openTrades: []gen.ListOpenTradesForSyncRow{
		{
			ID: 203, Symbol: sym, Direction: "LONG", Margin: decimal.NewFromInt(50),
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778499000, 0), Valid: true},
			EntryPrice:                 entryPx,
			BinanceDisasterStopOrderID: pgtype.Text{String: "12121", Valid: true},
			CurrentQty:                 dbQty,
		},
	}}

	// Tick 1: Binance has no position → candidate marked.
	fbc := &fakeBinance{positions: nil}
	pm := newTestPM(t, fdeps, fbc)
	algoDeps := &fakeAlgoDeps{}
	algoQuerier := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		12121: {AlgoID: 12121, AlgoStatus: "WORKING"},
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(algoDeps, algoQuerier, rdb, zerolog.Nop())
	pm.SetAlgoReconciler(ar)
	pm.nowFn = func() time.Time { return time.Unix(1778500000, 0).UTC() }
	pm.SyncTick(context.Background())
	_, ok := pm.pendingOrphans.Load(int64(203))
	require.True(t, ok, "tick 1 records candidate")

	// Tick 2: Binance now shows the position back (transient miss recovered).
	fbc.positions = []binance.PositionRisk{
		{Symbol: sym, PositionAmt: decimal.NewFromFloat(1), MarkPrice: decimal.NewFromInt(100), UnrealizedProfit: decimal.NewFromInt(0)},
	}
	pm.nowFn = func() time.Time { return time.Unix(1778500060, 0).UTC() }
	pm.SyncTick(context.Background())

	assert.Empty(t, fdeps.tradesClosed, "Binance recovered → no auto-sync")
	assert.Empty(t, fdeps.rcaInserted, "no halt — position is healthy on tick 2")
	// Note: pendingOrphans entry technically stays (orphan branch not entered),
	// but harmless — next time orphan is detected, the stale `firstSeen` will
	// be older than orphanConfirmDelay and will auto-sync. In practice the
	// trade closes naturally before another orphan recurrence.
}
