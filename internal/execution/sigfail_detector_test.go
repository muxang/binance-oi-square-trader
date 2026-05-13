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
		OIDropPct:   decimal.NewFromFloat(0.08),
		EMA20KLines: 5,
		Logic:       "AND",
	}
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

func setCloses(t *testing.T, mr *miniredis.Miniredis, sym string, closes ...string) {
	t.Helper()
	// Push newest-first (LPUSH semantic — but miniredis RPush; we'll use RPush newest last and read with LRange 0..n-1 → wrong direction)
	// Use RPush in order and the detector reads LRange 0..n-1, so first element = oldest.
	// Test should match production semantic: detector's getLastNCloses returns whatever LRange gives.
	// Production writer pushes newest-first via LPUSH. We mimic with RPush(reversed) → LRange returns newest-first.
	// For simplicity: just RPush closes; detector reads all; tests use closes that are all consistent (all below or all above EMA).
	for _, c := range closes {
		_, err := mr.Lpush("klines:closes:"+sym+":15m", c)
		require.NoError(t, err)
	}
}

// --- OI condition ---

func TestSigfail_OI_NoInitialOI_SkipsCondition(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 0)}, // initial_oi NULL
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400") // all below EMA
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	assert.Empty(t, closer.closed, "OI drop 5% < 8% threshold")
}

func TestSigfail_AND_BothTrigger_Fires(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	// initial=1000, current=900 → drop=10% > 8% ✓
	// closes all < EMA20 ✓
	deps := &fakeSigfailDeps{
		rows: []gen.ListOpenTradesForExitRow{mkSigfailRow(1, "BTCUSDT", 1000)},
		oi:   map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(900)},
	}
	setEMA(t, mr, "BTCUSDT", "80000")
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "OI drop + EMA break → AND fires")
	assert.Equal(t, int64(1), closer.closed[0].tradeID)
	assert.Equal(t, ExitReasonSigfail, closer.closed[0].exitReason)
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
	setCloses(t, mr, "BTCUSDT", "81000", "81100", "81200", "81300", "81400")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
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
	setCloses(t, mr, "BTCUSDT", "81000", "81100", "81200", "81300", "81400") // all above EMA
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "81000")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, defaultSigfailCfg())
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "drop == threshold (>= comparator) → fire")
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
	setCloses(t, mr, "BTCUSDT", "75000", "75100", "75200", "75300", "75400")
	closer := &fakeCloser{}
	sd := newTestSD(t, mr, deps, closer, cfg)
	sd.DetectTick(context.Background())
	require.Len(t, closer.closed, 1, "unknown logic falls back to AND (which fires here)")
}
