package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/storage/postgres/gen"
)

// --- fakes implementing the 3 minimal interfaces (CLAUDE.md §18) ---

type fakeWLQueries struct {
	squareRows  []gen.GetSquareMentionsTopRow
	squareErr   error
	oiRows      []gen.GetOIChangeTopRow
	oiErr       error
	priceRows   []gen.GetKlinesPriceChangeTopRow
	priceErr    error
	snapshotErr error

	mu              sync.Mutex
	snapshotCalled  int
	snapshotPayload []byte
}

func (q *fakeWLQueries) GetSquareMentionsTop(_ context.Context, _ gen.GetSquareMentionsTopParams) ([]gen.GetSquareMentionsTopRow, error) {
	return q.squareRows, q.squareErr
}
func (q *fakeWLQueries) GetOIChangeTop(_ context.Context, _ int32) ([]gen.GetOIChangeTopRow, error) {
	return q.oiRows, q.oiErr
}
func (q *fakeWLQueries) GetKlinesPriceChangeTop(_ context.Context, _ int32) ([]gen.GetKlinesPriceChangeTopRow, error) {
	return q.priceRows, q.priceErr
}
func (q *fakeWLQueries) InsertWatchlistSnapshot(_ context.Context, arg gen.InsertWatchlistSnapshotParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.snapshotCalled++
	q.snapshotPayload = arg.Symbols
	return q.snapshotErr
}

type fakeSymMeta struct {
	dates map[string]int64
	err   error
}

func (f *fakeSymMeta) GetOnboardDates(_ context.Context) (map[string]int64, error) {
	return f.dates, f.err
}

type fakeTickerSrc struct {
	data []binance.Ticker24hData
	err  error
}

func (f *fakeTickerSrc) FetchAll24hTicker(_ context.Context) ([]binance.Ticker24hData, error) {
	return f.data, f.err
}

// --- helpers ---

const fixedNowMs int64 = 1700000000000 // 2023-11-14 UTC, deterministic test clock

func daysAgo(d int) int64 { return fixedNowMs - int64(d)*86400000 }

func newWatchlistTest(t *testing.T) (*WatchlistCollector, *fakeWLQueries, *fakeSymMeta, *fakeTickerSrc, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	q := &fakeWLQueries{}
	sm := &fakeSymMeta{dates: map[string]int64{}}
	tk := &fakeTickerSrc{}
	c := NewWatchlistCollector(sm, tk, q, rdb, zerolog.Nop(), WatchlistCollectorConfig{
		Blacklist:             []string{"USDC"},
		LeverageTokenSuffixes: []string{"UPUSDT", "DOWNUSDT"},
	})
	c.nowFunc = func() time.Time { return time.UnixMilli(fixedNowMs).UTC() }
	return c, q, sm, tk, mr
}

// --- gatherSources (3) ---

func TestWatchlist_GatherSources_AllSucceed(t *testing.T) {
	c, q, _, _, _ := newWatchlistTest(t)
	q.squareRows = []gen.GetSquareMentionsTopRow{{Symbol: "BTCUSDT"}, {Symbol: "ETHUSDT"}}
	q.oiRows = []gen.GetOIChangeTopRow{{Symbol: "SOLUSDT"}}
	q.priceRows = []gen.GetKlinesPriceChangeTopRow{{Symbol: "DOGEUSDT"}}
	sources, err := c.gatherSources(context.Background(), c.nowFunc())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"BTCUSDT", "ETHUSDT"}, sources["square"])
	assert.ElementsMatch(t, []string{"SOLUSDT"}, sources["oi"])
	assert.ElementsMatch(t, []string{"DOGEUSDT"}, sources["price"])
	assert.Empty(t, sources["position"])
}

func TestWatchlist_GatherSources_SquareSQLError(t *testing.T) {
	c, q, _, _, _ := newWatchlistTest(t)
	q.squareErr = errors.New("db boom")
	_, err := c.gatherSources(context.Background(), c.nowFunc())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "square")
}

func TestWatchlist_GatherSources_OIEmpty_OthersOK(t *testing.T) {
	c, q, _, _, _ := newWatchlistTest(t)
	q.squareRows = []gen.GetSquareMentionsTopRow{{Symbol: "BTCUSDT"}}
	// q.oiRows empty (zero value)
	q.priceRows = []gen.GetKlinesPriceChangeTopRow{{Symbol: "ETHUSDT"}}
	sources, err := c.gatherSources(context.Background(), c.nowFunc())
	require.NoError(t, err, "empty source must not fail")
	assert.Len(t, sources["square"], 1)
	assert.Empty(t, sources["oi"])
	assert.Len(t, sources["price"], 1)
}

// --- mergeAndScore (3) ---

func TestWatchlist_Merge_DistinctSymbolsAcrossSources(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	candidates := c.mergeAndScore(map[string][]string{
		"square": {"AAA"}, "oi": {"BBB"}, "price": {"CCC"},
	})
	assert.Len(t, candidates, 3)
	for _, e := range candidates {
		assert.Equal(t, 1, e.Score)
		assert.Len(t, e.Sources, 1)
	}
}

func TestWatchlist_Merge_OverlappingSymbol_AccumulatesScore(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	candidates := c.mergeAndScore(map[string][]string{
		"square": {"BTCUSDT"},
		"oi":     {"BTCUSDT", "ETHUSDT"},
		"price":  {"BTCUSDT"},
	})
	var btc *symbolEntry
	for i := range candidates {
		if candidates[i].Symbol == "BTCUSDT" {
			btc = &candidates[i]
		}
	}
	require.NotNil(t, btc)
	assert.Equal(t, 3, btc.Score)
	assert.ElementsMatch(t, []string{"square", "oi", "price"}, btc.Sources)
}

func TestWatchlist_Merge_EmptyAllSources(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	candidates := c.mergeAndScore(map[string][]string{
		"square": nil, "oi": nil, "price": nil, "position": nil,
	})
	assert.Empty(t, candidates)
}

// --- applyFilters (4) ---

func filterFixture() (map[string]decimal.Decimal, map[string]int64) {
	return map[string]decimal.Decimal{
			"USDCUSDT": decimal.NewFromInt(50000000), "BTCUSDT": decimal.NewFromInt(100000000),
			"BTCUPUSDT": decimal.NewFromInt(50000000), "BTCDOWNUSDT": decimal.NewFromInt(50000000),
			"NEWUSDT": decimal.NewFromInt(50000000), "OLDUSDT": decimal.NewFromInt(50000000),
			"TINYUSDT": decimal.NewFromInt(5000000),
		}, map[string]int64{
			"USDCUSDT": daysAgo(100), "BTCUSDT": daysAgo(100),
			"BTCUPUSDT": daysAgo(100), "BTCDOWNUSDT": daysAgo(100),
			"NEWUSDT": daysAgo(3), "OLDUSDT": daysAgo(30),
			"TINYUSDT": daysAgo(100),
		}
}

func TestWatchlist_Filter_BlacklistRejected(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	tk, on := filterFixture()
	got := c.applyFilters([]symbolEntry{{Symbol: "USDCUSDT", Score: 1}, {Symbol: "BTCUSDT", Score: 1}}, tk, on, c.nowFunc())
	require.Len(t, got, 1)
	assert.Equal(t, "BTCUSDT", got[0].Symbol)
}

func TestWatchlist_Filter_LeverageTokenSuffixRejected(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	tk, on := filterFixture()
	got := c.applyFilters([]symbolEntry{
		{Symbol: "BTCUPUSDT", Score: 1}, {Symbol: "BTCDOWNUSDT", Score: 1}, {Symbol: "BTCUSDT", Score: 1},
	}, tk, on, c.nowFunc())
	require.Len(t, got, 1)
	assert.Equal(t, "BTCUSDT", got[0].Symbol)
}

func TestWatchlist_Filter_NewListingRejected(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	tk, on := filterFixture()
	got := c.applyFilters([]symbolEntry{{Symbol: "OLDUSDT", Score: 1}, {Symbol: "NEWUSDT", Score: 1}}, tk, on, c.nowFunc())
	require.Len(t, got, 1)
	assert.Equal(t, "OLDUSDT", got[0].Symbol)
}

func TestWatchlist_Filter_LowQuoteVolumeRejected(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	tk, on := filterFixture()
	got := c.applyFilters([]symbolEntry{{Symbol: "BTCUSDT", Score: 1}, {Symbol: "TINYUSDT", Score: 1}}, tk, on, c.nowFunc())
	require.Len(t, got, 1)
	assert.Equal(t, "BTCUSDT", got[0].Symbol)
}

// --- sortAndTruncate (2) ---

func TestWatchlist_Sort_DeterministicTieBreaker(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MaxSize = 10
	got := c.sortAndTruncate([]symbolEntry{
		{Symbol: "AAA", Score: 1, QuoteVolume: decimal.NewFromInt(100)},
		{Symbol: "BBB", Score: 2, QuoteVolume: decimal.NewFromInt(50)},  // highest score
		{Symbol: "CCC", Score: 1, QuoteVolume: decimal.NewFromInt(200)}, // tie score, higher qv
		{Symbol: "DDD", Score: 1, QuoteVolume: decimal.NewFromInt(100)}, // tie score+qv with AAA → ASC
	})
	assert.Equal(t, []string{"BBB", "CCC", "AAA", "DDD"}, []string{got[0].Symbol, got[1].Symbol, got[2].Symbol, got[3].Symbol})
}

func TestWatchlist_Truncate_AtMaxSize(t *testing.T) {
	c, _, _, _, _ := newWatchlistTest(t)
	c.cfg.MaxSize = 3
	candidates := make([]symbolEntry, 10)
	for i := range candidates {
		candidates[i] = symbolEntry{Symbol: fmt.Sprintf("S%02d", i), Score: 10 - i}
	}
	got := c.sortAndTruncate(candidates)
	assert.Len(t, got, 3)
}

// --- Run end-to-end (4) ---

func TestWatchlistRun_Success_WritesSnapshotAndRedis(t *testing.T) {
	c, q, sm, tk, mr := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	c.cfg.MaxSize = 100
	q.squareRows = []gen.GetSquareMentionsTopRow{{Symbol: "BTCUSDT"}}
	q.oiRows = []gen.GetOIChangeTopRow{{Symbol: "BTCUSDT"}}
	q.priceRows = []gen.GetKlinesPriceChangeTopRow{{Symbol: "BTCUSDT"}}
	sm.dates = map[string]int64{"BTCUSDT": daysAgo(100)}
	tk.data = []binance.Ticker24hData{{Symbol: "BTCUSDT", QuoteVolume: decimal.NewFromInt(100000000)}}

	require.NoError(t, c.Run(context.Background()))
	assert.Equal(t, 1, q.snapshotCalled)

	val, err := mr.Get("watchlist:current")
	require.NoError(t, err)
	var symbols []string
	require.NoError(t, json.Unmarshal([]byte(val), &symbols))
	assert.Equal(t, []string{"BTCUSDT"}, symbols)

	var pool []symbolEntry
	require.NoError(t, json.Unmarshal(q.snapshotPayload, &pool))
	require.Len(t, pool, 1)
	assert.Equal(t, 3, pool[0].Score, "BTCUSDT in 3 sources → score=3")
}

func TestWatchlistRun_NoCandidates_PreservesOldRedis(t *testing.T) {
	c, _, _, _, mr := newWatchlistTest(t)
	require.NoError(t, mr.Set("watchlist:current", `["OLDUSDT"]`))
	require.NoError(t, c.Run(context.Background()), "no-candidates path returns nil, not error")
	val, _ := mr.Get("watchlist:current")
	assert.Equal(t, `["OLDUSDT"]`, val, "old Redis must be preserved when no candidates")
}

func TestWatchlistRun_SnapshotFailure_SkipsRedis(t *testing.T) {
	c, q, sm, tk, mr := newWatchlistTest(t)
	c.cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	c.cfg.MinListingDays = 7
	q.squareRows = []gen.GetSquareMentionsTopRow{{Symbol: "BTCUSDT"}}
	sm.dates = map[string]int64{"BTCUSDT": daysAgo(100)}
	tk.data = []binance.Ticker24hData{{Symbol: "BTCUSDT", QuoteVolume: decimal.NewFromInt(100000000)}}
	q.snapshotErr = errors.New("db down")
	require.NoError(t, mr.Set("watchlist:current", `["OLDUSDT"]`))

	err := c.Run(context.Background())
	require.Error(t, err)
	val, _ := mr.Get("watchlist:current")
	assert.Equal(t, `["OLDUSDT"]`, val, "Redis must NOT be SET when snapshot fails (atomic order)")
}

func TestWatchlistRun_RedisFailure_LogsButReturnsNil(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	q := &fakeWLQueries{
		squareRows: []gen.GetSquareMentionsTopRow{{Symbol: "BTCUSDT"}},
	}
	sm := &fakeSymMeta{dates: map[string]int64{"BTCUSDT": daysAgo(100)}}
	tk := &fakeTickerSrc{data: []binance.Ticker24hData{{Symbol: "BTCUSDT", QuoteVolume: decimal.NewFromInt(100000000)}}}
	c := NewWatchlistCollector(sm, tk, q, rdb, zerolog.Nop(), WatchlistCollectorConfig{
		MinQuoteVolume: decimal.NewFromInt(10000000), MinListingDays: 7,
	})
	c.nowFunc = func() time.Time { return time.UnixMilli(fixedNowMs).UTC() }

	err := c.Run(context.Background())
	assert.NoError(t, err, "Redis SET failure must NOT fail Run (snapshot already persisted)")
	assert.Equal(t, 1, q.snapshotCalled, "snapshot must be persisted before Redis attempt")
}
