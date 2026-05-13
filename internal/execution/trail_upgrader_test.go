// v0.2 Round 1 Module B: TrailUpgrader unit tests covering 4-stage dispatch,
// trail_high ratchet, S2→S3 trader-managed switch, and S3/S4 re-arm logic.
package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
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

type fakeTrailDeps struct {
	rows       []gen.ListOpenTradesForTrailRow
	activates  []gen.UpdateTradeTrailActivateParams
	stages     []gen.UpdateTradeTrailStageParams
	highs      []gen.UpdateTradeTrailHighParams
	stageError error
}

func (f *fakeTrailDeps) ListOpenTradesForTrail(_ context.Context) ([]gen.ListOpenTradesForTrailRow, error) {
	return f.rows, nil
}
func (f *fakeTrailDeps) UpdateTradeTrailActivate(_ context.Context, arg gen.UpdateTradeTrailActivateParams) error {
	f.activates = append(f.activates, arg)
	return nil
}
func (f *fakeTrailDeps) UpdateTradeTrailStage(_ context.Context, arg gen.UpdateTradeTrailStageParams) error {
	f.stages = append(f.stages, arg)
	return f.stageError
}
func (f *fakeTrailDeps) UpdateTradeTrailHigh(_ context.Context, arg gen.UpdateTradeTrailHighParams) error {
	f.highs = append(f.highs, arg)
	return nil
}

type placedTrailing struct {
	symbol, qty, activation string
	callback                float64
}
type placedConditional struct {
	symbol, qty, trigger string
}

type fakeTrailBC struct {
	placedTrailing    []placedTrailing
	placedConditional []placedConditional
	cancelled         []int64
	cancelErr         map[int64]error
	nextAlgoID        int64
	placeTrailErr     error
	placeCondErr      error
}

func (f *fakeTrailBC) PlaceAlgoTrailingStop(_ context.Context, sym, qty, act string, cb float64) (binance.AlgoOrderResult, error) {
	if f.placeTrailErr != nil {
		return binance.AlgoOrderResult{}, f.placeTrailErr
	}
	f.placedTrailing = append(f.placedTrailing, placedTrailing{sym, qty, act, cb})
	f.nextAlgoID++
	return binance.AlgoOrderResult{AlgoID: f.nextAlgoID, Status: "WORKING"}, nil
}
func (f *fakeTrailBC) PlaceAlgoConditionalStop(_ context.Context, sym, qty, trigger string) (binance.AlgoOrderResult, error) {
	if f.placeCondErr != nil {
		return binance.AlgoOrderResult{}, f.placeCondErr
	}
	f.placedConditional = append(f.placedConditional, placedConditional{sym, qty, trigger})
	f.nextAlgoID++
	return binance.AlgoOrderResult{AlgoID: f.nextAlgoID, Status: "WORKING"}, nil
}
func (f *fakeTrailBC) CancelAlgoOrder(_ context.Context, _ string, algoID int64) error {
	if e, ok := f.cancelErr[algoID]; ok {
		return e
	}
	f.cancelled = append(f.cancelled, algoID)
	return nil
}

type fakeTickSize struct {
	filters map[string]binance.TradingFilters
}

func (f *fakeTickSize) GetTradingFilters(_ context.Context, sym string) (binance.TradingFilters, error) {
	if v, ok := f.filters[sym]; ok {
		return v, nil
	}
	return binance.TradingFilters{}, errors.New("symbol not found")
}

func defaultTrailCfg() TrailConfig {
	return TrailConfig{
		Stage1ActivatePct:  decimal.NewFromFloat(0.03),
		Stage1CallbackRate: decimal.NewFromFloat(0.03),
		Stage2UpgradePct:   decimal.NewFromFloat(0.15),
		Stage2CallbackRate: decimal.NewFromFloat(0.05),
		Stage3UpgradePct:   decimal.NewFromFloat(0.30),
		Stage3CallbackRate: decimal.NewFromFloat(0.10),
		Stage4UpgradePct:   decimal.NewFromFloat(0.60),
		Stage4CallbackRate: decimal.NewFromFloat(0.15),
	}
}

func newTestTU(t *testing.T, mr *miniredis.Miniredis, deps *fakeTrailDeps, bc *fakeTrailBC) *TrailUpgrader {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	tf := &fakeTickSize{filters: map[string]binance.TradingFilters{
		"BTCUSDT":  {TickSize: decimal.NewFromFloat(0.1)},
		"ALTUSDT":  {TickSize: decimal.NewFromFloat(0.0001)},
		"RIFUSDT":  {TickSize: decimal.NewFromFloat(0.0001)},
	}}
	return NewTrailUpgrader(deps, bc, tf, rdb, defaultTrailCfg(), zerolog.Nop())
}

func numFromFloat(f float64) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(decimal.NewFromFloat(f).String())
	return n
}

func mkTrailRow(id int64, sym string, entry, qty, trailHigh float64, stage int16, algoID string) gen.ListOpenTradesForTrailRow {
	r := gen.ListOpenTradesForTrailRow{
		ID:         id,
		Symbol:     sym,
		EntryPrice: numFromFloat(entry),
		Leverage:   10,
		TrailStage: stage,
		CurrentQty: numFromFloat(qty),
	}
	if trailHigh > 0 {
		r.TrailHighPrice = numFromFloat(trailHigh)
	}
	if algoID != "" {
		r.BinanceTrailAlgoID = pgtype.Text{String: algoID, Valid: true}
	}
	return r
}

func setLatest(t *testing.T, mr *miniredis.Miniredis, sym, price string) {
	t.Helper()
	require.NoError(t, mr.Set("latest_price:"+sym, price))
}

// --- S0 activation ---

func TestTrail_S0_BelowActivate_NoAction(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "82000") // entry 80000, +2.5% < 3%
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 0, 0, ""),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, deps.activates, "S0: +2.5% below activate → no S1 arm")
	assert.Empty(t, bc.placedTrailing)
}

func TestTrail_S0_AtActivate_PlacesS1(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "82400") // +3.0% exactly
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 0, 0, ""),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	require.Len(t, bc.placedTrailing, 1, "S0→S1: 1 TRAILING_STOP_MARKET placed")
	assert.Equal(t, "BTCUSDT", bc.placedTrailing[0].symbol)
	assert.Equal(t, "82400", bc.placedTrailing[0].activation, "rounded to 0.1 tick")
	assert.InDelta(t, 3.0, bc.placedTrailing[0].callback, 1e-9, "Binance callback 3% (×100 of 0.03)")
	require.Len(t, deps.activates, 1)
	assert.Equal(t, int64(1), deps.activates[0].ID)
}

// --- S1 → S2 upgrade ---

func TestTrail_S1_AtUpgrade_SwitchesToS2(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "92000") // +15% exactly
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 82400, 1, "100"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Equal(t, []int64{100}, bc.cancelled, "S1 algo cancelled")
	require.Len(t, bc.placedTrailing, 1, "new S2 TRAILING_STOP_MARKET placed")
	assert.InDelta(t, 5.0, bc.placedTrailing[0].callback, 1e-9, "S2 callback 5%")
	require.Len(t, deps.stages, 1)
	assert.Equal(t, int16(2), deps.stages[0].TrailStage)
}

func TestTrail_S1_BelowUpgrade_Persists(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "88000") // +10%, < 15%
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 82400, 1, "100"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled, "no upgrade")
	require.Len(t, deps.highs, 1, "trail_high persisted from 82400 → 88000")
	assert.True(t, deps.highs[0].TrailHighPrice.Equal(decimal.NewFromFloat(88000)))
}

// --- S2 → S3 (Binance → trader-managed) ---

func TestTrail_S2_AtUpgrade_SwitchesToS3_TraderManaged(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "104000") // +30%
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 102000, 2, "200"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Equal(t, []int64{200}, bc.cancelled, "S2 Binance native cancelled")
	require.Len(t, bc.placedConditional, 1, "trader-managed STOP_MARKET placed (NOT TRAILING)")
	assert.Empty(t, bc.placedTrailing, "S3 doesn't use TRAILING_STOP_MARKET")
	// new trail_high = max(102000, 104000) = 104000
	// stop = 104000 × (1 - 0.10) = 93600
	assert.Equal(t, "93600", bc.placedConditional[0].trigger)
	require.Len(t, deps.stages, 1)
	assert.Equal(t, int16(3), deps.stages[0].TrailStage)
}

// --- S3 → S4 ---

func TestTrail_S3_AtUpgrade_SwitchesToS4(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "128000") // +60%
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 120000, 3, "300"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Equal(t, []int64{300}, bc.cancelled, "S3 stop cancelled")
	require.Len(t, bc.placedConditional, 1, "S4 STOP_MARKET placed at high × 0.85")
	// trail_high = max(120000, 128000) = 128000; stop = 128000 × 0.85 = 108800
	assert.Equal(t, "108800", bc.placedConditional[0].trigger)
	require.Len(t, deps.stages, 1)
	assert.Equal(t, int16(4), deps.stages[0].TrailStage)
}

// --- S3 ratchet (trail_high moved up, no stage change) ---

func TestTrail_S3_HighMoved_Rearms(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "110000") // +37.5%, still < 60%; high moved from 105000 → 110000
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 105000, 3, "300"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Equal(t, []int64{300}, bc.cancelled, "old S3 stop cancelled for ratchet")
	require.Len(t, bc.placedConditional, 1, "new S3 stop placed at higher trigger")
	// new high = 110000; stop = 110000 × 0.90 = 99000
	assert.Equal(t, "99000", bc.placedConditional[0].trigger)
}

func TestTrail_S3_HighFlat_NoRearm(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "104000") // below stored high 105000
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 105000, 3, "300"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled, "high didn't advance → no ratchet")
	assert.Empty(t, bc.placedConditional)
}

// --- S4 ratchet ---

func TestTrail_S4_HighMoved_Rearms(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "150000")
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 130000, 4, "400"),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	// new high = 150000; stop = 150000 × 0.85 = 127500
	assert.Equal(t, []int64{400}, bc.cancelled)
	require.Len(t, bc.placedConditional, 1)
	assert.Equal(t, "127500", bc.placedConditional[0].trigger)
}

// --- Edge cases ---

func TestTrail_RedisLatestPriceMissing_SkipsRow(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// no setLatest call → Redis miss
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 0, 0, ""),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.placedTrailing, "no price → skip row, no action")
}

func TestTrail_QtyZero_SkipsRow(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "85000")
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0, 0, 0, ""), // qty=0 (TP fully closed)
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.placedTrailing, "qty=0 → skip (disaster stop covers)")
}

func TestTrail_S1Upgrade_CancelFails_AbortsUpgrade(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "92000")
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 82400, 1, "100"),
	}}
	bc := &fakeTrailBC{
		cancelErr: map[int64]error{100: errors.New("cancel rejected")},
	}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.placedTrailing, "cancel failed → no new arm (old S1 retained)")
	assert.Empty(t, deps.stages, "stage unchanged")
}

func TestTrail_S1Upgrade_PlaceFails_RollsBackToS0(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "92000")
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 82400, 1, "100"),
	}}
	bc := &fakeTrailBC{placeTrailErr: errors.New("binance 5xx")}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	require.Len(t, deps.stages, 1, "stage rolled back to S0 so next tick reactivates")
	assert.Equal(t, int16(0), deps.stages[0].TrailStage)
	assert.False(t, deps.stages[0].BinanceTrailAlgoID.Valid)
}

// --- Metrics ---

func TestTrail_S0_S1_Activation_IncrementsMetric(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	setLatest(t, mr, "BTCUSDT", "82400")
	before := testutil.ToFloat64(metrics.TrailingStageUpgradeTotal.WithLabelValues("0", "1"))
	deps := &fakeTrailDeps{rows: []gen.ListOpenTradesForTrailRow{
		mkTrailRow(1, "BTCUSDT", 80000, 0.01, 0, 0, ""),
	}}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	after := testutil.ToFloat64(metrics.TrailingStageUpgradeTotal.WithLabelValues("0", "1"))
	assert.Equal(t, 1.0, after-before, "metric incremented on S0→S1 activation")
}

func TestTrail_NoOpenTrades_NoCalls(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeTrailDeps{}
	bc := &fakeTrailBC{}
	tu := newTestTU(t, mr, deps, bc)
	tu.ReconcileTick(context.Background())
	assert.Empty(t, bc.placedTrailing)
	assert.Empty(t, bc.placedConditional)
}
