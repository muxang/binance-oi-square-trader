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
