package collector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/signal"
	"trader/internal/storage/postgres/gen"
)

// fakeSignalDataAccess implements signal.SignalDataAccess for unit tests.
type fakeSignalDataAccess struct {
	oiSeries      []decimal.Decimal
	hashtagSeries []decimal.Decimal
	closeNow      decimal.Decimal
	closePrior    decimal.Decimal

	failSymbol    string
	failSymbolErr error

	mu              chanmu // ensure inserted records counted under lock
	insertedCount   atomic.Int32
	insertedSymbols []string
	insertErr       error
}

// chanmu — minimal mutex via 1-buffered chan.
type chanmu chan struct{}

func newChanmu() chanmu  { return make(chan struct{}, 1) }
func (c chanmu) lock()   { c <- struct{}{} }
func (c chanmu) unlock() { <-c }

func (f *fakeSignalDataAccess) GetOIHistory(_ context.Context, symbol string, _ int) ([]decimal.Decimal, error) {
	if symbol == f.failSymbol {
		return nil, f.failSymbolErr
	}
	return f.oiSeries, nil
}
func (f *fakeSignalDataAccess) GetHashtagHistory(_ context.Context, _ string, _ int) ([]decimal.Decimal, error) {
	return f.hashtagSeries, nil
}
func (f *fakeSignalDataAccess) GetKlinesCloseNowAndPrior(_ context.Context, _ string, _ time.Duration) (decimal.Decimal, decimal.Decimal, error) {
	return f.closeNow, f.closePrior, nil
}
func (f *fakeSignalDataAccess) InsertSignal(_ context.Context, rec signal.SignalRecord) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.mu.lock()
	f.insertedSymbols = append(f.insertedSymbols, rec.Symbol)
	f.mu.unlock()
	f.insertedCount.Add(1)
	return nil
}

func newSignalEngineTestCollector(t *testing.T, deps *fakeSignalDataAccess) *SignalEngineCollector {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	deps.mu = newChanmu()
	return &SignalEngineCollector{
		redis:   rdb,
		deps:    deps,
		log:     zerolog.Nop(),
		cfg:     signalEngineDefaults(SignalEngineConfig{Concurrency: 2}),
		nowFunc: time.Now,
	}
}

func setEngineWatchlist(t *testing.T, c *SignalEngineCollector, symbols []string) {
	t.Helper()
	b, _ := json.Marshal(symbols)
	require.NoError(t, c.redis.Set(context.Background(), signalEngineWatchlistKey, b, 0).Err())
}

// --- 4 unit tests ---

func TestSignalEngine_EmptyWatchlist_Skips(t *testing.T) {
	deps := &fakeSignalDataAccess{}
	c := newSignalEngineTestCollector(t, deps)
	require.NoError(t, c.Run(context.Background()), "empty watchlist must skip not error")
	assert.EqualValues(t, 0, deps.insertedCount.Load(), "no insert when pool empty")
}

func TestSignalEngine_Run_LoopsPoolSymbols(t *testing.T) {
	// Insufficient OI data → all symbols decision=rejected, but Evaluate succeeds.
	// Test verifies fan-out + write loop, not algo correctness (compound_test covers algo).
	deps := &fakeSignalDataAccess{
		oiSeries:      decs(100, 101, 102), // < RecentPeriods 6 → insufficient_oi_history
		hashtagSeries: decs(100, 101, 102),
		closeNow:      decimal.NewFromInt(50000),
		closePrior:    decimal.NewFromInt(50000),
	}
	c := newSignalEngineTestCollector(t, deps)
	setEngineWatchlist(t, c, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"})
	require.NoError(t, c.Run(context.Background()))
	assert.EqualValues(t, 3, deps.insertedCount.Load(), "1 insert per pool symbol")
	assert.ElementsMatch(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, deps.insertedSymbols)
}

func TestSignalEngine_PerSymbolError_Isolated(t *testing.T) {
	// 1 symbol fails, others continue (Phase 1 模式)
	deps := &fakeSignalDataAccess{
		oiSeries:      decs(100, 101, 102),
		hashtagSeries: decs(100, 101, 102),
		closeNow:      decimal.NewFromInt(50000),
		closePrior:    decimal.NewFromInt(50000),
		failSymbol:    "ETHUSDT",
		failSymbolErr: errors.New("simulated PG hiccup"),
	}
	c := newSignalEngineTestCollector(t, deps)
	setEngineWatchlist(t, c, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"})
	require.NoError(t, c.Run(context.Background()), "1 symbol fail must not error tick")
	assert.EqualValues(t, 2, deps.insertedCount.Load(), "BTC + SOL written, ETH skipped")
	assert.NotContains(t, deps.insertedSymbols, "ETHUSDT")
}

func TestSignalEngine_AllSymbolsFailEvalErr_DoesntPanic(t *testing.T) {
	deps := &fakeSignalDataAccess{
		failSymbol:    "*", // will not match any actual symbol; we use insertErr to force failures globally
		insertErr:     errors.New("disk full"),
		oiSeries:      decs(100, 101, 102),
		hashtagSeries: decs(100, 101, 102),
		closeNow:      decimal.NewFromInt(50000),
		closePrior:    decimal.NewFromInt(50000),
	}
	c := newSignalEngineTestCollector(t, deps)
	setEngineWatchlist(t, c, []string{"BTCUSDT", "ETHUSDT"})
	require.NoError(t, c.Run(context.Background()), "all-fail must not panic the tick")
	assert.EqualValues(t, 0, deps.insertedCount.Load())
}

// decs is the local helper (signal pkg's d/dseries are in signal_test, not exported).
func decs(vs ...float64) []decimal.Decimal {
	out := make([]decimal.Decimal, len(vs))
	for i, v := range vs {
		out[i] = decimal.NewFromFloat(v)
	}
	return out
}

// --- 3 adapter integration tests (opt-in INTEGRATION_PG=1) ---

func TestSignalEngineAdapter_GetOIHistory_DescToAsc(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationPG(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	q := gen.New(tx)
	const sym = "ADAPTERTESTUSDT"
	// Insert 3 rows DESC-distinct timestamps
	for _, off := range []int64{0, 5, 10} {
		_, err := tx.Exec(ctx, "INSERT INTO oi_history (symbol, ts, oi, oi_value_usd) VALUES ($1, $2, $3, $4)",
			sym, time.Now().UTC().Add(-time.Duration(off)*time.Minute), decimal.NewFromInt(off+100), decimal.NewFromInt(off+1000))
		require.NoError(t, err)
	}
	a := &signalDataAccess{queries: q, log: zerolog.Nop(), nowFunc: time.Now}
	out, err := a.GetOIHistory(ctx, sym, 10)
	require.NoError(t, err)
	require.Len(t, out, 3)
	// ASC: oldest first (ts -10min) ↑ to newest (ts -0min). OI values were 100/105/110 inserted at ts -0/-5/-10.
	// So oldest ts -10min has oi=110, newest ts -0min has oi=100. ASC order: [110, 105, 100].
	assert.True(t, out[0].Equal(decimal.NewFromInt(110)), "ASC[0]=oldest ts row, oi=110, got %s", out[0])
	assert.True(t, out[2].Equal(decimal.NewFromInt(100)), "ASC[2]=newest ts row, oi=100, got %s", out[2])
}

func TestSignalEngineAdapter_GetKlinesCloseNowAndPrior_PicksClosest(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationPG(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	q := gen.New(tx)
	const sym = "KLINESTESTUSDT"
	// Insert 5 bars at -0/-15/-30/-45/-60 min with distinct close
	now := time.Now().UTC().Truncate(15 * time.Minute)
	for i, off := range []int{0, 15, 30, 45, 60} {
		_, err := tx.Exec(ctx, "INSERT INTO klines (symbol, timeframe, open_time, open, high, low, close, volume, quote_volume) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
			sym, "15m", now.Add(-time.Duration(off)*time.Minute),
			decimal.NewFromInt(int64(50000+i)), decimal.NewFromInt(int64(51000+i)),
			decimal.NewFromInt(int64(49000+i)), decimal.NewFromInt(int64(50500+i)),
			decimal.NewFromInt(100), decimal.NewFromInt(50000),
		)
		require.NoError(t, err)
	}
	a := &signalDataAccess{queries: q, log: zerolog.Nop(), nowFunc: time.Now}
	closeNow, closePrior, err := a.GetKlinesCloseNowAndPrior(ctx, sym, 60*time.Minute)
	require.NoError(t, err)
	assert.True(t, closeNow.Equal(decimal.NewFromInt(50500)), "closeNow = i=0 bar (50500), got %s", closeNow)
	assert.True(t, closePrior.Equal(decimal.NewFromInt(50504)), "closePrior = -60min bar (i=4, 50504), got %s", closePrior)
}

func TestSignalEngineAdapter_InsertSignal_RoundTrip(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationPG(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	q := gen.New(tx)
	a := &signalDataAccess{queries: q, log: zerolog.Nop(), nowFunc: time.Now}
	rec := signal.SignalRecord{
		Ts: time.Now().UTC(), Symbol: "ADAPTERSIG",
		OITriggered: true, OIData: signal.OISurgeResult{Triggered: true, GrowingPeriods: 5},
		SquareHot: true, SquareData: signal.SquareHotResult{Hot: true, Mode: signal.ModeStandard},
		Decision: "entered_full",
	}
	require.NoError(t, a.InsertSignal(ctx, rec))
	var oiData []byte
	err = tx.QueryRow(ctx, "SELECT oi_data FROM signals WHERE symbol = $1", "ADAPTERSIG").Scan(&oiData)
	require.NoError(t, err)
	// PG JSONB normalizes whitespace + key order — parse + assert semantics not raw string.
	var parsed signal.OISurgeResult
	require.NoError(t, json.Unmarshal(oiData, &parsed))
	assert.True(t, parsed.Triggered)
	assert.Equal(t, 5, parsed.GrowingPeriods)
}

func openIntegrationPG(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_PG_DSN")
	if dsn == "" {
		dsn = "postgres://trader:trader@127.0.0.1:5432/trader?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	return pool
}
