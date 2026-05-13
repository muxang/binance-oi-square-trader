// v0.2 Round 3 Module C: SigfailDetector unit tests — OI drop + EMA20 break
// + AND/OR logic + data-availability edge cases.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/storage/postgres/gen"
)

type fakeSigfailDeps struct {
	rows    []gen.ListOpenTradesForExitRow
	listErr error
	oi      map[string]decimal.Decimal
	oiErr   map[string]error
	// Round 3.x: PG-backed closes + low window.
	closes    map[string][]decimal.Decimal // symbol → []close (newest first)
	closesErr map[string]error
	low       map[string]decimal.Decimal // symbol → window low
	lowErr    map[string]error
}

func (f *fakeSigfailDeps) ListOpenTradesForExit(_ context.Context) ([]gen.ListOpenTradesForExitRow, error) {
	return f.rows, f.listErr
}
func (f *fakeSigfailDeps) GetLatestOI(_ context.Context, sym string) (decimal.Decimal, error) {
	if e, ok := f.oiErr[sym]; ok {
		return decimal.Zero, e
	}
	if v, ok := f.oi[sym]; ok {
		return v, nil
	}
	return decimal.Zero, errors.New("oi not found")
}
func (f *fakeSigfailDeps) GetLastNCloses(_ context.Context, arg gen.GetLastNClosesParams) ([]decimal.Decimal, error) {
	if e, ok := f.closesErr[arg.Symbol]; ok {
		return nil, e
	}
	if v, ok := f.closes[arg.Symbol]; ok {
		if int(arg.Limit) < len(v) {
			return v[:arg.Limit], nil
		}
		return v, nil
	}
	return nil, nil
}
func (f *fakeSigfailDeps) GetLowestLowSince(_ context.Context, arg gen.GetLowestLowSinceParams) (decimal.Decimal, error) {
	if e, ok := f.lowErr[arg.Symbol]; ok {
		return decimal.Zero, e
	}
	if v, ok := f.low[arg.Symbol]; ok {
		return v, nil
	}
	return decimal.Zero, nil
}

type fakeCloser struct {
	closed []closeCall
}
type closeCall struct {
	tradeID    int64
	exitReason string
}

func (f *fakeCloser) ClosePosition(_ context.Context, t gen.ListOpenTradesForExitRow, exitReason string, _ zerolog.Logger) {
	f.closed = append(f.closed, closeCall{tradeID: t.ID, exitReason: exitReason})
}

func defaultSigfailCfg() SigfailConfig {
	return SigfailConfig{
		OIDropPct:         decimal.NewFromFloat(0.08),
		EMA20KLines:       5,
		Logic:             "AND",
		LowBreakBufferPct: decimal.NewFromFloat(0.005),
		LowLookbackMin:    30,
	}
}

// makeCloses parses string closes into decimals (newest first).
func makeCloses(t *testing.T, vals ...string) []decimal.Decimal {
	t.Helper()
	out := make([]decimal.Decimal, 0, len(vals))
	for _, v := range vals {
		d, err := decimal.NewFromString(v)
		require.NoError(t, err)
		out = append(out, d)
	}
	return out
}

func newTestSD(t *testing.T, mr *miniredis.Miniredis, deps *fakeSigfailDeps, closer *fakeCloser, cfg SigfailConfig) *SigfailDetector {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewSigfailDetector(deps, closer, rdb, cfg, zerolog.Nop())
}

func numFromDec(d decimal.Decimal) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(d.String())
	return n
}

func mkSigfailRow(id int64, sym string, initialOI float64) gen.ListOpenTradesForExitRow {
	r := gen.ListOpenTradesForExitRow{
		ID:     id,
		Symbol: sym,
	}
	if initialOI > 0 {
		r.InitialOI = numFromDec(decimal.NewFromFloat(initialOI))
	}
	return r
}

func setEMA(t *testing.T, mr *miniredis.Miniredis, sym, val string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"value": val, "computed_at": "2026-05-13T00:00:00Z"})
	require.NoError(t, mr.Set("ema20:"+sym, string(payload)))
}

// setClosesPG populates the fake deps.closes map (Round 3.x: PG-backed).
// Closes are newest-first (matches PG query ORDER BY open_time DESC).
func setClosesPG(t *testing.T, deps *fakeSigfailDeps, sym string, closes []decimal.Decimal) {
	t.Helper()
	if deps.closes == nil {
		deps.closes = map[string][]decimal.Decimal{}
	}
	deps.closes[sym] = closes
}

// setLow seeds the fake deps for condition C window low. Pair with setLatest
// (Redis latest_price) to evaluate the price-low-break formula end-to-end.
func setLow(t *testing.T, deps *fakeSigfailDeps, sym, low string) {
	t.Helper()
	if deps.low == nil {
		deps.low = map[string]decimal.Decimal{}
	}
	d, err := decimal.NewFromString(low)
	require.NoError(t, err)
	deps.low[sym] = d
}

// (setLatest already defined in trail_upgrader_test.go — same package; reuse.)

// --- OI condition ---

func TestSigfail_OI_NoInitialOI_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 0)}, // initial_oi NULL
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400")) // all below EMA
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "no initial_oi + AND logic → must not fire even if EMA triggers")
}

func TestSigfail_OI_DropBelowThreshold_NoFire(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// initial=1000, current=950 → drop=5% < 8%
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(950)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "OI drop 5% < 8% threshold")
}

func TestSigfail_AND_AllThreeTrigger_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// A: drop=10% > 8% ✓
	// B: closes all < EMA20 ✓
	// C: current 79000 < window_low 80000 × 0.995 = 79600 → trigger ✓
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	setLow(t, deps, "BTCUSDT", "80000")
	setLatest(t, mr, "BTCUSDT", "79000")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "all 3 triggers → AND fires")
	assert.Equal(t, ExitReasonSigfail, closer.closed[0].exitReason)
}

func TestSigfail_AND_MissingConditionC_NoFire(t *testing.T) {
	// A + B trigger but C data unavailable → AND must NOT fire (Round 3.x gate).
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	// No latest_price, no low → condition C unavailable
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "AND with 1 condition skipped → no fire (data quality gate)")
}

func TestSigfail_AND_OnlyOITrig_NoFire(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// OI drops 10% ✓, but closes ALL above EMA (no EMA trigger)
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "81000", "81100", "81200", "81300", "81400"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "AND: only OI triggers → no fire")
}

func TestSigfail_AND_OnlyEMATrig_NoFire(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// OI no drop (current = initial), closes all below EMA ✓
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "AND: only EMA triggers → no fire")
}

func TestSigfail_OR_OnlyOITrig_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "OR"
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)}, // 10% drop
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "81000", "81100", "81200", "81300", "81400")) // all above EMA
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "OR + OI drop only → fire")
}

func TestSigfail_OR_OnlyEMATrig_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "OR"
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)}, // no drop
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "OR + EMA break only → fire")
}

func TestSigfail_EMA_NotAllClosesBelow_NoFire(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	// 4 below + 1 above → EMA condition NOT met (must be ALL N)
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "81000"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "1 of 5 closes above EMA → no EMA trigger")
}

func TestSigfail_EMA_InsufficientCloses_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	// Only 3 closes when we need 5
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "insufficient closes → EMA skipped → AND no fire")
}

func TestSigfail_OI_FetchError_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows:  []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oiErr: map[string]error{"BTCUSDT": errors.New("pg timeout")},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "OI fetch error → OI skipped → AND no fire")
}

func TestSigfail_NoOpenTrades_NoCalls(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, &fakeSigfailDeps{}, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed)
}

func TestSigfail_BoundaryOIExactly8pct_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// initial=1000, current=920 → drop=8.0% exactly (boundary)
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(920)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	setLow(t, deps, "BTCUSDT", "80000")
	setLatest(t, mr, "BTCUSDT", "79000") // < 79600 trigger
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "all 3 triggers + drop == threshold → fire")
}

// --- Round 3.x: condition C (price low break) ---

func TestSigfail_OR_OnlyLowBreakTrig_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "OR"
	// A: no drop, B: closes above EMA, C: current 79000 < 80000×0.995=79600 → trigger
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "81000", "81100", "81200", "81300", "81400"))
	setLow(t, deps, "BTCUSDT", "80000")
	setLatest(t, mr, "BTCUSDT", "79000")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "OR + only condition C → fire")
}

func TestSigfail_LowBreak_NoBreakWithBuffer(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "OR"
	// current 79700 > threshold 79600 → no trigger
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "81000", "81100", "81200", "81300", "81400"))
	setLow(t, deps, "BTCUSDT", "80000")
	setLatest(t, mr, "BTCUSDT", "79700") // > 79600 threshold
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "current within buffer of low → no trigger")
}

func TestSigfail_LowBreak_NoLatestPrice_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "OR"
	// latest_price not set in Redis → condition C should skip; OR fires on OI alone
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setLow(t, deps, "BTCUSDT", "80000")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "no latest_price → C skipped; OR fires on OI")
}

func TestSigfail_LowBreak_EmptyWindow_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "AND"
	// Window low = 0 (no bars in lookback) → C skipped → AND must NOT fire
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	// deps.low not set for BTCUSDT → GetLowestLowSince returns zero
	setLatest(t, mr, "BTCUSDT", "79000")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "empty window → C skipped → AND no fire")
}

func TestSigfail_UnknownLogic_DefaultsToAND(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	cfg := defaultSigfailCfg()
	cfg.Logic = "UNKNOWN"
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setClosesPG(t, deps, "BTCUSDT", makeCloses(t, "75000", "75100", "75200", "75300", "75400"))
	setLow(t, deps, "BTCUSDT", "80000")
	setLatest(t, mr, "BTCUSDT", "79000")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "unknown logic falls back to AND (3 triggers → fire)")
}
