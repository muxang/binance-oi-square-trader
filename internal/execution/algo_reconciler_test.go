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

type fakeAlgoQuerier struct {
	resp map[int64]binance.AlgoOrderQuery
	err  map[int64]error
}

func (f *fakeAlgoQuerier) QueryAlgoOrder(_ context.Context, algoID int64) (binance.AlgoOrderQuery, error) {
	if e, ok := f.err[algoID]; ok {
		return binance.AlgoOrderQuery{}, e
	}
	return f.resp[algoID], nil
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
