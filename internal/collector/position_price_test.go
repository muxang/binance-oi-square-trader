package collector

import (
	"context"
	"errors"
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

// --- fakes (CLAUDE.md §18 — minimal interfaces, defined in consumer) ---

type fakeResponse struct {
	data binance.MarkPriceData
	err  error
}

type fakeMarkPriceFetcher struct {
	mu        sync.Mutex
	calls     map[string]int
	responses map[string][]fakeResponse
}

func newFakeFetcher() *fakeMarkPriceFetcher {
	return &fakeMarkPriceFetcher{calls: map[string]int{}, responses: map[string][]fakeResponse{}}
}

func (f *fakeMarkPriceFetcher) setResponse(symbol string, resps ...fakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[symbol] = resps
}

func (f *fakeMarkPriceFetcher) FetchMarkPrice(_ context.Context, symbol string) (binance.MarkPriceData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.calls[symbol]
	f.calls[symbol] = n + 1
	seq := f.responses[symbol]
	if len(seq) == 0 {
		return binance.MarkPriceData{}, errors.New("no fake response configured for " + symbol)
	}
	if n >= len(seq) {
		return seq[len(seq)-1].data, seq[len(seq)-1].err
	}
	return seq[n].data, seq[n].err
}

func (f *fakeMarkPriceFetcher) callCount(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[symbol]
}

func (f *fakeMarkPriceFetcher) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.calls {
		n += v
	}
	return n
}

type fakePositionTradeQueries struct {
	trades []gen.GetOpenTradesRow
	err    error
}

func (f *fakePositionTradeQueries) GetOpenTrades(_ context.Context) ([]gen.GetOpenTradesRow, error) {
	return f.trades, f.err
}

// --- helpers ---

func tradeRow(symbol string) gen.GetOpenTradesRow {
	return gen.GetOpenTradesRow{Symbol: symbol, Status: "open"}
}

func newPositionPriceTest(t *testing.T) (*PositionPriceCollector, *fakeMarkPriceFetcher, *fakePositionTradeQueries, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	fetcher := newFakeFetcher()
	queries := &fakePositionTradeQueries{}
	cfg := positionPriceDefaults(PositionPriceConfig{
		RetryInterval:    1 * time.Millisecond, // fast retries
		PerSymbolTimeout: 500 * time.Millisecond,
		PerTickTimeout:   30 * time.Second,
	})
	c := &PositionPriceCollector{
		fetcher: fetcher, queries: queries, redis: rdb,
		log: zerolog.Nop(), cfg: cfg, nowFunc: time.Now,
	}
	return c, fetcher, queries, mr
}

// --- Run, 0 持仓 (1) ---

func TestPositionPriceRun_NoOpenTrades_ImmediateReturn(t *testing.T) {
	c, fetcher, queries, mr := newPositionPriceTest(t)
	queries.trades = nil
	require.NoError(t, c.Run(context.Background()))
	assert.Equal(t, 0, fetcher.totalCalls(), "0 positions must NOT trigger any FetchMarkPrice")
	assert.Empty(t, mr.Keys(), "0 positions must NOT write any Redis keys")
}

// --- Run 有持仓 (3) ---

func TestPositionPriceRun_SingleSymbol_WritesRedis(t *testing.T) {
	c, fetcher, queries, mr := newPositionPriceTest(t)
	queries.trades = []gen.GetOpenTradesRow{tradeRow("BTCUSDT")}
	fetcher.setResponse("BTCUSDT", fakeResponse{data: binance.MarkPriceData{Symbol: "BTCUSDT", MarkPrice: decimal.RequireFromString("80300.5")}})
	require.NoError(t, c.Run(context.Background()))
	val, err := mr.Get("latest_price:BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, "80300.5", val)
	assert.Equal(t, 5*time.Minute, mr.TTL("latest_price:BTCUSDT"), "TTL must be 5min per ARCH §7")
}

func TestPositionPriceRun_MultiSymbol_AllSucceed(t *testing.T) {
	c, fetcher, queries, mr := newPositionPriceTest(t)
	queries.trades = []gen.GetOpenTradesRow{tradeRow("BTCUSDT"), tradeRow("ETHUSDT"), tradeRow("SOLUSDT")}
	fetcher.setResponse("BTCUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(80000)}})
	fetcher.setResponse("ETHUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(3000)}})
	fetcher.setResponse("SOLUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(150)}})
	require.NoError(t, c.Run(context.Background()))
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		_, err := mr.Get("latest_price:" + sym)
		require.NoError(t, err, "expected key for %s", sym)
	}
}

func TestPositionPriceRun_DuplicateSymbols_Deduped(t *testing.T) {
	c, fetcher, queries, _ := newPositionPriceTest(t)
	// One symbol in 2 trades (long + hedge / 2 entries) must dedup to 1 call.
	queries.trades = []gen.GetOpenTradesRow{tradeRow("BTCUSDT"), tradeRow("BTCUSDT"), tradeRow("ETHUSDT")}
	fetcher.setResponse("BTCUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(80000)}})
	fetcher.setResponse("ETHUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(3000)}})
	require.NoError(t, c.Run(context.Background()))
	assert.Equal(t, 1, fetcher.callCount("BTCUSDT"), "BTCUSDT in 2 trades but must dedup to 1 call")
	assert.Equal(t, 1, fetcher.callCount("ETHUSDT"))
}

// --- uniqueSymbols (2) ---

func TestUniqueSymbols_DistinctSymbols(t *testing.T) {
	out := uniqueSymbols([]gen.GetOpenTradesRow{tradeRow("BTCUSDT"), tradeRow("ETHUSDT"), tradeRow("SOLUSDT")})
	assert.Equal(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, out)
}

func TestUniqueSymbols_DuplicatesPreserveFirstOrder(t *testing.T) {
	out := uniqueSymbols([]gen.GetOpenTradesRow{
		tradeRow("BTCUSDT"), tradeRow("ETHUSDT"), tradeRow("BTCUSDT"), tradeRow("SOLUSDT"), tradeRow("ETHUSDT"),
	})
	assert.Equal(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, out, "must preserve first-seen order")
}

// --- fetchSingleMarkPrice retry (5) ---

func TestFetchSingleMarkPrice_Success_NoRetry(t *testing.T) {
	c, fetcher, _, _ := newPositionPriceTest(t)
	fetcher.setResponse("BTCUSDT", fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(80000)}})
	data, err := c.fetchSingleMarkPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.True(t, data.MarkPrice.Equal(decimal.NewFromInt(80000)))
	assert.Equal(t, 1, fetcher.callCount("BTCUSDT"))
}

func TestFetchSingleMarkPrice_TransientError_Retries(t *testing.T) {
	c, fetcher, _, _ := newPositionPriceTest(t)
	fetcher.setResponse("BTCUSDT",
		fakeResponse{err: errors.New("network err 1")},
		fakeResponse{err: errors.New("network err 2")},
		fakeResponse{data: binance.MarkPriceData{MarkPrice: decimal.NewFromInt(80000)}},
	)
	data, err := c.fetchSingleMarkPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.True(t, data.MarkPrice.Equal(decimal.NewFromInt(80000)))
	assert.Equal(t, 3, fetcher.callCount("BTCUSDT"), "retry until success at attempt 3")
}

func TestFetchSingleMarkPrice_AllAttemptsFail_ReturnsError(t *testing.T) {
	c, fetcher, _, _ := newPositionPriceTest(t)
	fetcher.setResponse("BTCUSDT", fakeResponse{err: errors.New("network err")})
	_, err := c.fetchSingleMarkPrice(context.Background(), "BTCUSDT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 2 retries")
	assert.Equal(t, 3, fetcher.callCount("BTCUSDT"), "1 + 2 retries = 3 attempts")
}

func TestFetchSingleMarkPrice_4xx_NoRetry(t *testing.T) {
	c, fetcher, _, _ := newPositionPriceTest(t)
	fetcher.setResponse("BTCUSDT", fakeResponse{err: &binance.APIError{HTTPCode: 400, Message: "bad symbol"}})
	_, err := c.fetchSingleMarkPrice(context.Background(), "BTCUSDT")
	require.Error(t, err)
	var apiErr *binance.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 400, apiErr.HTTPCode)
	assert.Equal(t, 1, fetcher.callCount("BTCUSDT"), "4xx must NOT retry")
}

func TestFetchSingleMarkPrice_CtxCancelled_ExitsImmediately(t *testing.T) {
	c, fetcher, _, _ := newPositionPriceTest(t)
	c.cfg.RetryInterval = 10 * time.Second // long enough that ctx cancel is the only early exit
	fetcher.setResponse("BTCUSDT", fakeResponse{err: errors.New("network err")})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.fetchSingleMarkPrice(ctx, "BTCUSDT")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 2*time.Second, "ctx cancel must cut 10s retry interval short")
}

// --- writeRedis (2) ---

func TestWriteRedis_PartialSuccess_CountsCorrectly(t *testing.T) {
	c, _, _, mr := newPositionPriceTest(t)
	results := []markPriceResult{
		{symbol: "BTCUSDT", markPrice: decimal.NewFromInt(80000), err: nil},
		{symbol: "ETHUSDT", err: errors.New("fetch failed")},
		{symbol: "SOLUSDT", markPrice: decimal.NewFromInt(150), err: nil},
	}
	success := c.writeRedis(context.Background(), results)
	assert.Equal(t, 2, success)
	_, err := mr.Get("latest_price:BTCUSDT")
	assert.NoError(t, err)
	_, err = mr.Get("latest_price:ETHUSDT")
	assert.Error(t, err, "ETHUSDT must NOT be written (fetch failed)")
	_, err = mr.Get("latest_price:SOLUSDT")
	assert.NoError(t, err)
}

func TestWriteRedis_AllFailed_NoEnqueue(t *testing.T) {
	c, _, _, mr := newPositionPriceTest(t)
	results := []markPriceResult{
		{symbol: "BTCUSDT", err: errors.New("e1")},
		{symbol: "ETHUSDT", err: errors.New("e2")},
	}
	success := c.writeRedis(context.Background(), results)
	assert.Equal(t, 0, success)
	assert.Empty(t, mr.Keys(), "no Redis keys when all results errored")
}
