// v0.2 Gap 1: AlgoReconciler unit tests covering ReconcileTick dispatch
// (WORKING/FINISHED/CANCELED) + autoClose persistence + edge cases.
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

type fakeAlgoDeps struct {
	openTrades    []gen.ListOpenTradesWithAlgoRow
	openTradesErr error
	exits         []gen.InsertTradeExitParams
	closed        []gen.UpdateTradeClosedParams
	stateDeleted  []int64
	cbUpdated     []gen.UpdateAfterTradeCloseParams
	// Round 2 Module A partial-close artifacts:
	qtyDecremented []gen.DecrementPositionQtyParams
	tpCleared      []gen.ClearTPAlgoIDParams
	dailyPartial   []gen.UpdateDailyPnlPartialParams
	// R.26 -2013 fallback artifacts
	trailCleared    []int64
	disasterCleared []int64
}

func (f *fakeAlgoDeps) ListOpenTradesWithAlgo(_ context.Context) ([]gen.ListOpenTradesWithAlgoRow, error) {
	return f.openTrades, f.openTradesErr
}
func (f *fakeAlgoDeps) InsertTradeExitIdempotent(_ context.Context, arg gen.InsertTradeExitParams) (int64, error) {
	f.exits = append(f.exits, arg)
	return 1, nil // simulate successful insert (no conflict)
}
func (f *fakeAlgoDeps) UpdateTradeClosed(_ context.Context, arg gen.UpdateTradeClosedParams) error {
	f.closed = append(f.closed, arg)
	return nil
}
func (f *fakeAlgoDeps) DeletePositionState(_ context.Context, tradeID int64) error {
	f.stateDeleted = append(f.stateDeleted, tradeID)
	return nil
}
func (f *fakeAlgoDeps) UpdateAfterTradeClose(_ context.Context, arg gen.UpdateAfterTradeCloseParams) error {
	f.cbUpdated = append(f.cbUpdated, arg)
	return nil
}
func (f *fakeAlgoDeps) DecrementPositionQty(_ context.Context, arg gen.DecrementPositionQtyParams) error {
	f.qtyDecremented = append(f.qtyDecremented, arg)
	return nil
}
func (f *fakeAlgoDeps) ClearTPAlgoID(_ context.Context, arg gen.ClearTPAlgoIDParams) error {
	f.tpCleared = append(f.tpCleared, arg)
	return nil
}
func (f *fakeAlgoDeps) UpdateDailyPnlPartial(_ context.Context, arg gen.UpdateDailyPnlPartialParams) error {
	f.dailyPartial = append(f.dailyPartial, arg)
	return nil
}
func (f *fakeAlgoDeps) ClearTrailAlgoID(_ context.Context, id int64) error {
	f.trailCleared = append(f.trailCleared, id)
	return nil
}
func (f *fakeAlgoDeps) ClearDisasterStopOrderID(_ context.Context, id int64) error {
	f.disasterCleared = append(f.disasterCleared, id)
	return nil
}

type fakeAlgoQuerier struct {
	resp        map[int64]binance.AlgoOrderQuery
	err         map[int64]error
	positions   map[string][]binance.PositionRisk
	positionErr error
	// R.27: batch-list endpoint used to disambiguate testnet's broken
	// single-algo GET. Map of algoID → present (existence indicates "still
	// NEW/WORKING on Binance"). listErr forces failure for the safety-default
	// test path.
	openAlgos map[int64]binance.AlgoOpenOrder
	listErr   error
}

func (f *fakeAlgoQuerier) QueryAlgoOrder(_ context.Context, algoID int64) (binance.AlgoOrderQuery, error) {
	if e, ok := f.err[algoID]; ok {
		return binance.AlgoOrderQuery{}, e
	}
	return f.resp[algoID], nil
}

// R.26: stub for the position-diff reconcile path used by reconcileTPGone.
func (f *fakeAlgoQuerier) GetPositionRisk(_ context.Context, symbol string) ([]binance.PositionRisk, error) {
	if f.positionErr != nil {
		return nil, f.positionErr
	}
	return f.positions[symbol], nil
}

// R.27: batch endpoint to second-confirm -2013. Empty map = nothing open
// (i.e. the queried algo is truly gone), matching most legacy R.26 tests.
func (f *fakeAlgoQuerier) ListOpenAlgoOrders(_ context.Context) ([]binance.AlgoOpenOrder, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]binance.AlgoOpenOrder, 0, len(f.openAlgos))
	for _, a := range f.openAlgos {
		out = append(out, a)
	}
	return out, nil
}

func newTestAR(deps *fakeAlgoDeps, bc *fakeAlgoQuerier) *AlgoReconciler {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"}) // unreachable, ZREM logs only
	ar := NewAlgoReconciler(deps, bc, rdb, zerolog.Nop())
	ar.nowFn = func() time.Time { return time.Unix(1778700000, 0).UTC() }
	return ar
}

func mkAlgoRow(id int64, symbol string, entryPrice float64, qty float64, leverage int16, algoID string) gen.ListOpenTradesWithAlgoRow {
	ep := pgtype.Numeric{}
	_ = ep.Scan(decimal.NewFromFloat(entryPrice).String())
	q := pgtype.Numeric{}
	_ = q.Scan(decimal.NewFromFloat(qty).String())
	return gen.ListOpenTradesWithAlgoRow{
		ID:                         id,
		Symbol:                     symbol,
		EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778500000, 0).UTC(), Valid: true},
		EntryPrice:                 ep,
		Margin:                     decimal.NewFromInt(50),
		Leverage:                   leverage,
		BinanceDisasterStopOrderID: pgtype.Text{String: algoID, Valid: true},
		CurrentQty:                 q,
	}
}

// mkTrailRow builds a row with both disaster + trail algos set (Round 1.x).
// trailAlgoID empty → no trail algo. trailStage 0–4 maps to trail_sN exit_reason.
func mkAlgoRowWithTrail(id int64, symbol string, entryPrice, qty float64, disasterID, trailID string, trailStage int16) gen.ListOpenTradesWithAlgoRow {
	r := mkAlgoRow(id, symbol, entryPrice, qty, 10, disasterID)
	if trailID != "" {
		r.BinanceTrailAlgoID = pgtype.Text{String: trailID, Valid: true}
	}
	r.TrailStage = trailStage
	return r
}

func TestReconcileTick_NoOpenTrades(t *testing.T) {
	deps := &fakeAlgoDeps{}
	bc := &fakeAlgoQuerier{}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits)
	assert.Empty(t, deps.closed)
}

func TestReconcileTick_AlgoWorking_NoClose(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(1, "BTCUSDT", 80000, 0.006, 10, "12345"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		12345: {AlgoID: 12345, Symbol: "BTCUSDT", AlgoStatus: "WORKING"},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.closed, "WORKING → no close")
	assert.Empty(t, deps.exits)
}

func TestReconcileTick_AlgoNew_NoClose(t *testing.T) {
	// Round 1.z: NEW = armed waiting (Binance Algo Service initial state before
	// trigger condition met). Must be treated as actionable (no WRN log), and
	// no close until status flips to FINISHED.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(2, "BTCUSDT", 80000, 0.006, 10, "12346"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		12346: {AlgoID: 12346, Symbol: "BTCUSDT", AlgoStatus: "NEW"},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.closed, "NEW → no close (waiting for trigger)")
	assert.Empty(t, deps.exits)
}

func TestReconcileTick_AlgoFinished_AutoCloses(t *testing.T) {
	const sym = "ALGOTRIG1"
	metrics.PositionMarginRatio.WithLabelValues(sym).Set(0.55)
	require.InDelta(t, 0.55, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9)

	// Entry 80000, qty 0.006, Algo fired at actualPrice 75200 → loss
	// realized_pnl = (75200 - 80000) × 0.006 = -28.8
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(70, sym, 80000, 0.006, 10, "98765"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		98765: {
			AlgoID: 98765, Symbol: sym, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(75200),
			Quantity:    decimal.NewFromFloat(0.006),
			TriggerTime: time.Unix(1778699950, 0).UTC(),
		},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 1, "FINISHED → InsertTradeExit")
	assert.Equal(t, ExitReasonDisaster, deps.exits[0].Type)
	assert.True(t, deps.exits[0].Price.Equal(decimal.NewFromFloat(75200)), "exit price = Algo actualPrice")
	assert.True(t, deps.exits[0].Pnl.Equal(decimal.NewFromFloat(-28.8)), "realized_pnl = (75200-80000) × 0.006 = -28.8")

	require.Len(t, deps.closed, 1, "FINISHED → UpdateTradeClosed")
	assert.Equal(t, int64(70), deps.closed[0].ID)
	assert.Equal(t, ExitReasonDisaster, deps.closed[0].ExitReason.String)
	assert.True(t, deps.closed[0].RealizedPnl.Equal(decimal.NewFromFloat(-28.8)))

	require.Len(t, deps.stateDeleted, 1, "position_states deleted")
	assert.Equal(t, int64(70), deps.stateDeleted[0])

	require.Len(t, deps.cbUpdated, 1, "circuit_breaker rollup")
	assert.True(t, deps.cbUpdated[0].RealizedPnl.Equal(decimal.NewFromFloat(-28.8)))

	assert.InDelta(t, 0.0, testutil.ToFloat64(metrics.PositionMarginRatio.WithLabelValues(sym)), 1e-9,
		"margin_ratio gauge cleared on auto-close (v0.2 Catch 2 parity)")
}

func TestReconcileTick_AlgoCanceled_NoCloseLogsOnly(t *testing.T) {
	// CANCELED means Algo gone but trade still open. position_manager
	// local_only_orphan branch will pick it up — reconciler must NOT close.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(80, "OPUSDT", 5, 100, 10, "55555"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		55555: {AlgoID: 55555, Symbol: "OPUSDT", AlgoStatus: "CANCELED"},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits, "CANCELED → no close (position_manager handles via orphan)")
	assert.Empty(t, deps.closed)
}

func TestReconcileTick_QueryError_SkipsRowContinues(t *testing.T) {
	// 2 trades: first errs on Query (skip), second is WORKING (no-op).
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(91, "BTCUSDT", 80000, 0.006, 10, "11111"),
		mkAlgoRow(92, "ETHUSDT", 2000, 0.5, 10, "22222"),
	}}
	bc := &fakeAlgoQuerier{
		err:  map[int64]error{11111: errors.New("network timeout")},
		resp: map[int64]binance.AlgoOrderQuery{22222: {AlgoStatus: "WORKING"}},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits, "row 91 errored, row 92 working → no closes")
}

func TestReconcileTick_FinishedZeroActualPrice_FallsBackToTriggerPrice(t *testing.T) {
	// Defensive: Algo response sometimes has actualPrice=0 even when FINISHED
	// (Binance anomaly). Fall back to triggerPrice.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(100, "BTCUSDT", 80000, 0.006, 10, "33333"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		33333: {
			AlgoStatus:   "FINISHED",
			ActualPrice:  decimal.Zero, // anomaly
			TriggerPrice: decimal.NewFromFloat(75200),
			Quantity:     decimal.NewFromFloat(0.006),
		},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1, "fallback closes")
	assert.True(t, deps.exits[0].Price.Equal(decimal.NewFromFloat(75200)),
		"fallback uses triggerPrice when actualPrice is 0")
}

func TestReconcileTick_FinishedBothPricesZero_SkipsToAvoidGarbagePnl(t *testing.T) {
	// Both actualPrice and triggerPrice zero — would compute pnl from
	// (0 - entry) × qty = -big garbage. Skip, retry next tick.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(110, "BTCUSDT", 80000, 0.006, 10, "44444"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		44444: {AlgoStatus: "FINISHED"}, // both prices zero
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits, "zero prices → skip, next tick retries (defensive)")
	assert.Empty(t, deps.closed)
}

func TestReconcileTick_InvalidAlgoID_SkipsRow(t *testing.T) {
	// Defensive: algo_id from DB is non-numeric (shouldn't happen but log+skip).
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		{
			ID: 120, Symbol: "BTCUSDT", Margin: decimal.NewFromInt(50), Leverage: 10,
			BinanceDisasterStopOrderID: pgtype.Text{String: "not-a-number", Valid: true},
			EntryTs:                    pgtype.Timestamptz{Time: time.Unix(1778500000, 0).UTC(), Valid: true},
		},
	}}
	bc := &fakeAlgoQuerier{}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits, "invalid algo_id → skip row, no Query attempt")
}

// --- Round 1.x: trail algo FINISHED auto-reconcile ---

func TestReconcileTick_TrailS1_Finished_ClosesWithTrailS1Reason(t *testing.T) {
	// Disaster algo WORKING, trail S1 algo FINISHED → close with exit_reason='trail_s1'.
	// Entry 80000, qty 0.006, trail filled at 84000 → pnl = (84000-80000) × 0.006 = 24
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(70, "BTCUSDT", 80000, 0.006, "1000", "2000", 1),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		1000: {AlgoID: 1000, Symbol: "BTCUSDT", AlgoStatus: "WORKING"},
		2000: {AlgoID: 2000, Symbol: "BTCUSDT", AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(84000), Quantity: decimal.NewFromFloat(0.006),
			TriggerTime: time.Unix(1778699950, 0).UTC()},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 1, "trail FINISHED triggers one close")
	assert.Equal(t, ExitReasonTrailS1, deps.exits[0].Type, "exit_reason='trail_s1'")
	assert.True(t, deps.exits[0].Price.Equal(decimal.NewFromFloat(84000)))
	assert.True(t, deps.exits[0].Pnl.Equal(decimal.NewFromFloat(24)))

	require.Len(t, deps.closed, 1)
	assert.Equal(t, ExitReasonTrailS1, deps.closed[0].ExitReason.String, "trades.exit_reason='trail_s1'")
}

func TestReconcileTick_TrailS3_Finished_UsesS3Reason(t *testing.T) {
	// Stage 3 (trader-managed STOP_MARKET) FINISHED → trail_s3.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(71, "RIFUSDT", 0.1, 1000, "", "3000", 3),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		3000: {AlgoID: 3000, Symbol: "RIFUSDT", AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(0.117), Quantity: decimal.NewFromFloat(1000)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1)
	assert.Equal(t, ExitReasonTrailS3, deps.exits[0].Type)
}

func TestReconcileTick_TrailS4_Finished_UsesS4Reason(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(72, "SOLUSDT", 100, 1.0, "", "4000", 4),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		4000: {AlgoID: 4000, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(150), Quantity: decimal.NewFromFloat(1.0)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1)
	assert.Equal(t, ExitReasonTrailS4, deps.exits[0].Type)
}

func TestReconcileTick_TrailCanceled_NoClose(t *testing.T) {
	// Trader-initiated cancel during S1→S2 upgrade is normal: info-level log, no close.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(73, "BTCUSDT", 80000, 0.006, "", "5000", 1),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		5000: {AlgoID: 5000, AlgoStatus: "CANCELED"},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits, "trail CANCELED → no close (upgrade race or external)")
}

func TestReconcileTick_DisasterFinished_TrailUnpolled(t *testing.T) {
	// If disaster FINISHED first, skip trail poll (trade already closed).
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(74, "BTCUSDT", 80000, 0.006, "6000", "7000", 1),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		6000: {AlgoID: 6000, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(75200), Quantity: decimal.NewFromFloat(0.006)},
		7000: {AlgoID: 7000, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(84000), Quantity: decimal.NewFromFloat(0.006)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1, "only disaster close fires; trail skipped (trade already closed)")
	assert.Equal(t, ExitReasonDisaster, deps.exits[0].Type)
}

func TestReconcileTick_TrailOnly_NoDisasterAlgoID(t *testing.T) {
	// Trade with only trail algo set (disaster placement failed earlier).
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(75, "BTCUSDT", 80000, 0.006, "", "8000", 2),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		8000: {AlgoID: 8000, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(92000), Quantity: decimal.NewFromFloat(0.006)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1)
	assert.Equal(t, ExitReasonTrailS2, deps.exits[0].Type, "trail_stage=2 → trail_s2 reason")
}

func TestTrailExitReasonForStage(t *testing.T) {
	assert.Equal(t, ExitReasonTrailS1, trailExitReasonForStage(0), "stage 0 defaults to S1 reason (defensive)")
	assert.Equal(t, ExitReasonTrailS1, trailExitReasonForStage(1))
	assert.Equal(t, ExitReasonTrailS2, trailExitReasonForStage(2))
	assert.Equal(t, ExitReasonTrailS3, trailExitReasonForStage(3))
	assert.Equal(t, ExitReasonTrailS4, trailExitReasonForStage(4))
}

// --- Round 2 Module A: TP partial close ---

// mkAlgoRowWithTPs adds tp1/tp2 algo IDs on top of mkAlgoRowWithTrail.
func mkAlgoRowWithTPs(id int64, symbol string, entry, qty float64, tp1ID, tp2ID string) gen.ListOpenTradesWithAlgoRow {
	r := mkAlgoRow(id, symbol, entry, qty, 10, "")
	if tp1ID != "" {
		r.BinanceTP1AlgoID = pgtype.Text{String: tp1ID, Valid: true}
	}
	if tp2ID != "" {
		r.BinanceTP2AlgoID = pgtype.Text{String: tp2ID, Valid: true}
	}
	return r
}

func TestReconcileTick_TP1_Finished_PartialClose(t *testing.T) {
	// Entry 80000, qty 0.01, TP1 fires at 88000 (entry × 1.10).
	// Algo placed with qty 0.002 (20% of 0.01). pnl = (88000-80000) × 0.002 = 16.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTPs(80, "BTCUSDT", 80000, 0.01, "9001", ""),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9001: {AlgoID: 9001, Symbol: "BTCUSDT", AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(88000), Quantity: decimal.NewFromFloat(0.002)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 1, "TP1 FINISHED → partial exit row")
	assert.Equal(t, ExitReasonTP1, deps.exits[0].Type)
	assert.True(t, deps.exits[0].Pnl.Equal(decimal.NewFromFloat(16)))
	assert.True(t, deps.exits[0].Qty.Equal(decimal.NewFromFloat(0.002)))

	// Trade NOT fully closed — no UpdateTradeClosed call.
	assert.Empty(t, deps.closed, "TP partial close MUST NOT mark trade closed")
	assert.Empty(t, deps.stateDeleted, "position_states preserved (still open)")
	assert.Empty(t, deps.cbUpdated, "consec_losses untouched (only daily_pnl bumped)")

	require.Len(t, deps.qtyDecremented, 1)
	assert.True(t, deps.qtyDecremented[0].Delta.Equal(decimal.NewFromFloat(0.002)))

	require.Len(t, deps.tpCleared, 1)
	assert.Equal(t, "tp1", deps.tpCleared[0].Type)

	require.Len(t, deps.dailyPartial, 1, "daily_pnl partial update")
}

func TestReconcileTick_TP2_Finished_PartialClose(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTPs(81, "BTCUSDT", 80000, 0.01, "", "9002"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9002: {AlgoID: 9002, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(100000), Quantity: decimal.NewFromFloat(0.002)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	require.Len(t, deps.exits, 1)
	assert.Equal(t, ExitReasonTP2, deps.exits[0].Type)
	require.Len(t, deps.tpCleared, 1)
	assert.Equal(t, "tp2", deps.tpCleared[0].Type)
}

func TestReconcileTick_TP1_Then_Trail_SameTick_RunningQty(t *testing.T) {
	// Both fire same tick. TP1 partial (0.002) then trail full close with remaining 0.008.
	// Entry 80000, total qty 0.01. TP1 fills @ 88000 (pnl=(88000-80000)*0.002=16).
	// Trail S1 fills @ 84000 (pnl=(84000-80000)*0.008=32) on remaining qty.
	r := mkAlgoRowWithTPs(82, "BTCUSDT", 80000, 0.01, "9100", "")
	r.BinanceTrailAlgoID = pgtype.Text{String: "9101", Valid: true}
	r.TrailStage = 1
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{r}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9100: {AlgoID: 9100, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(88000), Quantity: decimal.NewFromFloat(0.002)},
		9101: {AlgoID: 9101, AlgoStatus: "FINISHED",
			ActualPrice: decimal.NewFromFloat(84000), Quantity: decimal.NewFromFloat(0.01)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 2, "TP1 + trail_s1 = 2 exit rows")
	// Order: TP1 first, then trail (per ReconcileTick dispatch order).
	assert.Equal(t, ExitReasonTP1, deps.exits[0].Type)
	assert.True(t, deps.exits[0].Pnl.Equal(decimal.NewFromFloat(16)), "TP1 pnl on 0.002")
	assert.Equal(t, ExitReasonTrailS1, deps.exits[1].Type)
	assert.True(t, deps.exits[1].Pnl.Equal(decimal.NewFromFloat(32)),
		"trail pnl on running qty 0.008 (= 0.01 - 0.002 TP1)")
	require.Len(t, deps.closed, 1, "trail fully closes")
}

func TestReconcileTick_TP1_TP2_Disaster_SameTick(t *testing.T) {
	// Worst-case same-tick: TP1 + TP2 + disaster all FINISHED.
	// Entry 80000, qty 0.01. TP1 fills 0.002 @ 88000. TP2 fills 0.002 @ 100000.
	// Disaster fills remaining 0.006 @ 75200.
	// Disaster pnl = (75200-80000) × 0.006 = -28.8
	r := mkAlgoRowWithTPs(83, "BTCUSDT", 80000, 0.01, "9200", "9201")
	r.BinanceDisasterStopOrderID = pgtype.Text{String: "9202", Valid: true}
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{r}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9200: {AlgoStatus: "FINISHED", ActualPrice: decimal.NewFromFloat(88000), Quantity: decimal.NewFromFloat(0.002)},
		9201: {AlgoStatus: "FINISHED", ActualPrice: decimal.NewFromFloat(100000), Quantity: decimal.NewFromFloat(0.002)},
		9202: {AlgoStatus: "FINISHED", ActualPrice: decimal.NewFromFloat(75200), Quantity: decimal.NewFromFloat(0.01)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 3, "TP1 + TP2 + disaster = 3 exit rows")
	assert.Equal(t, ExitReasonTP1, deps.exits[0].Type)
	assert.Equal(t, ExitReasonTP2, deps.exits[1].Type)
	assert.Equal(t, ExitReasonDisaster, deps.exits[2].Type)
	assert.True(t, deps.exits[2].Pnl.Equal(decimal.NewFromFloat(-28.8)),
		"disaster pnl on running 0.006 (0.01 - 0.002 - 0.002 TPs)")
	require.Len(t, deps.closed, 1, "disaster fully closes; trail not polled (continue)")
}

func TestReconcileTick_TP1_Duplicate_NoDoubleDecrement(t *testing.T) {
	// Simulate idempotency: InsertTradeExitIdempotent returns 0 → don't decrement again.
	// We force this by pre-populating exits then re-running on the same row.
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTPs(84, "BTCUSDT", 80000, 0.01, "9300", ""),
	}}
	// Override InsertTradeExitIdempotent to simulate ON CONFLICT (return 0).
	deps2 := &dupDeps{fakeAlgoDeps: deps}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9300: {AlgoStatus: "FINISHED", ActualPrice: decimal.NewFromFloat(88000), Quantity: decimal.NewFromFloat(0.002)},
	}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379"})
	ar := NewAlgoReconciler(deps2, bc, rdb, zerolog.Nop())
	ar.nowFn = func() time.Time { return time.Unix(1778700000, 0).UTC() }
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.qtyDecremented, "idempotent skip → no double decrement")
	assert.Empty(t, deps.tpCleared, "idempotent skip → no DB updates")
}

// dupDeps wraps fakeAlgoDeps but forces InsertTradeExitIdempotent to return 0
// (simulates ON CONFLICT idempotent skip).
type dupDeps struct{ *fakeAlgoDeps }

func (d *dupDeps) InsertTradeExitIdempotent(_ context.Context, arg gen.InsertTradeExitParams) (int64, error) {
	d.exits = append(d.exits, arg)
	return 0, nil
}

func TestReconcileTick_TP_NotFinished_NoAction(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTPs(85, "BTCUSDT", 80000, 0.01, "9400", "9401"),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9400: {AlgoStatus: "WORKING"},
		9401: {AlgoStatus: "WORKING"},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	assert.Empty(t, deps.exits)
	assert.Empty(t, deps.qtyDecremented)
}

func TestReconcileTick_TPMetric_Incremented(t *testing.T) {
	before := testutil.ToFloat64(metrics.TPFilledTotal.WithLabelValues("METRICSYM", "tp1"))
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTPs(86, "METRICSYM", 100, 1, "9500", ""),
	}}
	bc := &fakeAlgoQuerier{resp: map[int64]binance.AlgoOrderQuery{
		9500: {AlgoStatus: "FINISHED", ActualPrice: decimal.NewFromFloat(110), Quantity: decimal.NewFromFloat(0.2)},
	}}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())
	after := testutil.ToFloat64(metrics.TPFilledTotal.WithLabelValues("METRICSYM", "tp1"))
	assert.Equal(t, 1.0, after-before, "TPFilledTotal incremented on TP1 partial close")
}

// --- R.26: -2013 "Order does not exist" fallback ---

// mkTPRowWithStopPx is like mkAlgoRowWithTPs but also sets initial_take_profit_1/2
// so the R.26 -2013 fallback has a synthetic fill price to use.
func mkTPRowWithStopPx(id int64, symbol string, entry, qty, tp1Px, tp2Px float64, tp1ID, tp2ID string) gen.ListOpenTradesWithAlgoRow {
	r := mkAlgoRowWithTPs(id, symbol, entry, qty, tp1ID, tp2ID)
	if tp1Px > 0 {
		r.InitialTakeProfit1 = decimal.NullDecimal{Decimal: decimal.NewFromFloat(tp1Px), Valid: true}
	}
	if tp2Px > 0 {
		r.InitialTakeProfit2 = decimal.NullDecimal{Decimal: decimal.NewFromFloat(tp2Px), Valid: true}
	}
	return r
}

// errNotFound matches Binance -2013 error shape produced by binance.Client.
var errNotFound = &binance.APIError{HTTPCode: 400, BizCode: -2013, Message: "Order does not exist."}

// Scenario mirrors IDUSDT #294: TP1 was placed for 20% × DB qty, fired, Binance
// position dropped, but the algo query returns -2013. The R.26 fallback must:
//   1. read positionRisk to learn current Binance qty
//   2. compute fill_qty = db_qty − binance_qty
//   3. use trades.initial_take_profit_1 as the synthetic fill price
//   4. call partialClose → writes exit row + decrements DB qty + clears algo_id
func TestReconcileTick_TP1_Gone2013_ReconcilesFromPositionDiff(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		// entry 0.026, DB qty 4443, TP1 stop 0.03124 (entry × 1.20 say), algo 9700.
		mkTPRowWithStopPx(294, "IDUSDT", 0.026, 4443, 0.03124, 0, "9700", ""),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9700: errNotFound},
		positions: map[string][]binance.PositionRisk{
			"IDUSDT": {{Symbol: "IDUSDT", PositionAmt: decimal.NewFromInt(3555)}}, // dropped by 888
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	require.Len(t, deps.exits, 1, "must write 1 exit row from position diff")
	got := deps.exits[0]
	assert.Equal(t, "tp1", got.Type)
	assert.True(t, got.Qty.Equal(decimal.NewFromInt(888)), "fill_qty = db(4443) − binance(3555) = 888, got %s", got.Qty.String())
	assert.True(t, got.Price.Equal(decimal.NewFromFloat(0.03124)), "uses stored stop price as synthetic fill price")
	// pnl = (0.03124 − 0.026) × 888 = 4.65312
	wantPnl := decimal.NewFromFloat(0.03124).Sub(decimal.NewFromFloat(0.026)).Mul(decimal.NewFromInt(888))
	assert.True(t, got.Pnl.Equal(wantPnl), "pnl=%s want=%s", got.Pnl.String(), wantPnl.String())

	require.Len(t, deps.qtyDecremented, 1)
	assert.True(t, deps.qtyDecremented[0].Delta.Equal(decimal.NewFromInt(888)))
	require.Len(t, deps.tpCleared, 1)
	assert.Equal(t, int64(294), deps.tpCleared[0].ID)
	assert.Equal(t, "tp1", deps.tpCleared[0].Type)
}

// -2013 with no position diff = either cancelled or we're racing the fire moment.
// Clear algo_id (so we stop polling), don't write exit row.
func TestReconcileTick_TP1_Gone2013_NoPositionDiff_ClearsOnly(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkTPRowWithStopPx(295, "FOOUSDT", 1.0, 100, 1.1, 0, "9701", ""),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9701: errNotFound},
		positions: map[string][]binance.PositionRisk{
			"FOOUSDT": {{Symbol: "FOOUSDT", PositionAmt: decimal.NewFromInt(100)}}, // unchanged
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits, "no exit row when position unchanged")
	assert.Empty(t, deps.qtyDecremented, "no qty decrement when position unchanged")
	require.Len(t, deps.tpCleared, 1, "still clears algo_id to stop polling")
	assert.Equal(t, "tp1", deps.tpCleared[0].Type)
}

// -2013 with missing stop price = can't compute pnl. Clear algo_id only.
func TestReconcileTick_TP1_Gone2013_MissingStopPx_ClearsOnly(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		// tp1Px=0 → InitialTakeProfit1 is Null in the row
		mkTPRowWithStopPx(296, "BARUSDT", 1.0, 100, 0, 0, "9702", ""),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9702: errNotFound},
		positions: map[string][]binance.PositionRisk{
			"BARUSDT": {{Symbol: "BARUSDT", PositionAmt: decimal.NewFromInt(80)}},
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits, "no exit row without stop price (would have garbage pnl)")
	require.Len(t, deps.tpCleared, 1, "still clears algo_id to stop the -2013 polling loop")
}

// Trail -2013 must NOT auto-close (fire price unknown). Just clear trail algo_id;
// orphan_sync (R.8) handles position state.
func TestReconcileTick_Trail_Gone2013_ClearsTrailOnly(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(297, "BAZUSDT", 1.0, 100, "", "9800", 1),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9800: errNotFound},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits, "trail -2013 doesn't write exit row (price unknown)")
	assert.Empty(t, deps.closed, "trade not closed by reconciler — orphan_sync handles")
	require.Len(t, deps.trailCleared, 1)
	assert.Equal(t, int64(297), deps.trailCleared[0])
}

// Disaster -2013 same shape as trail: clear, defer to orphan_sync.
func TestReconcileTick_Disaster_Gone2013_ClearsDisasterOnly(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(298, "QUXUSDT", 1.0, 100, 10, "9900"),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9900: errNotFound},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits)
	assert.Empty(t, deps.closed)
	require.Len(t, deps.disasterCleared, 1)
	assert.Equal(t, int64(298), deps.disasterCleared[0])
}

// HasFinishedTPForTrade halt-suppression: -2013 on TP query means "treat as
// filled" so position_manager skips the drift_halt for that tick. Without this,
// every fired TP on testnet triggered a 30-45min drift_halt loop.
func TestHasFinishedTPForTrade_Gone2013_ReturnsTrue(t *testing.T) {
	deps := &fakeAlgoDeps{}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9999: errNotFound},
	}
	ar := newTestAR(deps, bc)
	assert.True(t, ar.HasFinishedTPForTrade(context.Background(), "9999", ""))
}

func TestHasFinishedTPForTrade_GenericError_ReturnsFalse(t *testing.T) {
	deps := &fakeAlgoDeps{}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{8888: assert.AnError},
	}
	ar := newTestAR(deps, bc)
	assert.False(t, ar.HasFinishedTPForTrade(context.Background(), "8888", ""))
}

// --- R.27: -2013 false-positive (algo healthy in batch list) ---

// Reproduces COAIUSDT #300: testnet returns -2013 for the per-id GET 1min after
// placement, but ListOpenAlgoOrders shows the algo is still NEW. Reconciler MUST
// skip and NOT clear the algo_id — clearing would tear down stop protection.
func TestReconcileTick_TP1_Gone2013_StillInBatchList_DoesNotClear(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkTPRowWithStopPx(310, "COAIUSDT", 0.34, 369, 0.3749, 0, "9710", ""),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9710: errNotFound},
		// Algo IS in the batch list — still healthy on Binance.
		openAlgos: map[int64]binance.AlgoOpenOrder{
			9710: {AlgoID: 9710, Symbol: "COAIUSDT", Status: "NEW", Side: "SELL", ReduceOnly: true},
		},
		positions: map[string][]binance.PositionRisk{
			"COAIUSDT": {{Symbol: "COAIUSDT", PositionAmt: decimal.NewFromInt(369)}},
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits, "no exit row when algo healthy")
	assert.Empty(t, deps.qtyDecremented, "no qty decrement when algo healthy")
	assert.Empty(t, deps.tpCleared, "must NOT clear algo_id when it's still NEW in batch list")
}

// Trail/disaster false-positive: same logic — must not clear.
func TestReconcileTick_Trail_Gone2013_StillInBatchList_DoesNotClear(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRowWithTrail(311, "COAIUSDT", 0.34, 369, "", "9810", 1),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9810: errNotFound},
		openAlgos: map[int64]binance.AlgoOpenOrder{
			9810: {AlgoID: 9810, Symbol: "COAIUSDT", Status: "NEW"},
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.trailCleared, "must NOT clear trail algo_id when healthy in batch")
	assert.Empty(t, deps.exits)
	assert.Empty(t, deps.closed)
}

// Disaster -2013 false-positive: same.
func TestReconcileTick_Disaster_Gone2013_StillInBatchList_DoesNotClear(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkAlgoRow(312, "COAIUSDT", 0.34, 369, 5, "9910"),
	}}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{9910: errNotFound},
		openAlgos: map[int64]binance.AlgoOpenOrder{
			9910: {AlgoID: 9910, Symbol: "COAIUSDT", Status: "NEW"},
		},
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.disasterCleared, "must NOT clear disaster_stop when healthy in batch")
	assert.Empty(t, deps.closed)
}

// Batch endpoint itself fails → safety default: don't clear, don't reconcile.
// Next tick retries; algo remains DB-tracked.
func TestReconcileTick_TP1_Gone2013_ListErrFail_SkipsTick(t *testing.T) {
	deps := &fakeAlgoDeps{openTrades: []gen.ListOpenTradesWithAlgoRow{
		mkTPRowWithStopPx(313, "FOOUSDT", 1.0, 100, 1.1, 0, "9720", ""),
	}}
	bc := &fakeAlgoQuerier{
		err:     map[int64]error{9720: errNotFound},
		listErr: assert.AnError,
	}
	ar := newTestAR(deps, bc)
	ar.ReconcileTick(context.Background())

	assert.Empty(t, deps.exits, "list endpoint error → no reconcile")
	assert.Empty(t, deps.tpCleared, "list endpoint error → don't clear (safety default)")
}

// HasFinishedTPForTrade with -2013 + algo healthy in batch: must NOT suppress
// halt (returning true would mask real drift). Returns false so position_manager
// proceeds with its drift evaluation.
func TestHasFinishedTPForTrade_Gone2013_StillInBatchList_ReturnsFalse(t *testing.T) {
	deps := &fakeAlgoDeps{}
	bc := &fakeAlgoQuerier{
		err: map[int64]error{7777: errNotFound},
		openAlgos: map[int64]binance.AlgoOpenOrder{
			7777: {AlgoID: 7777, Status: "NEW"},
		},
	}
	ar := newTestAR(deps, bc)
	assert.False(t, ar.HasFinishedTPForTrade(context.Background(), "7777", ""),
		"algo healthy in batch → don't suppress halt")
}
