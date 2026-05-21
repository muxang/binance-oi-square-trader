package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/coingecko"
	"trader/internal/storage/postgres/gen"
)

const marketsBatchFixture = `[
  {"id":"bitcoin","symbol":"btc","current_price":80000.0,"market_cap":1576000000000,"circulating_supply":19700000},
  {"id":"ethereum","symbol":"eth","current_price":4200.0,"market_cap":504000000000,"circulating_supply":120000000}
]`

// supplyTestCapture holds the three independent write streams the collector
// emits per tick (full-market cache + legacy lh row update).
type supplyTestCapture struct {
	cache  []gen.UpsertCoingeckoMarketCacheParams
	lhRows []gen.UpdateLatestMarketCapForSymbolParams
}

func newTestSupplyCollector(
	t *testing.T,
	mappings []gen.ListCoingeckoMappingsRow,
	oiByBin map[string]decimal.Decimal,
	h http.Handler,
) (*CirculatingSupplyCollector, *supplyTestCapture) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	mu := sync.Mutex{}
	cap := &supplyTestCapture{}

	c := &CirculatingSupplyCollector{
		cg:  coingecko.NewTestClient(srv.URL),
		log: zerolog.Nop(),
		listMapFn: func(ctx context.Context) ([]gen.ListCoingeckoMappingsRow, error) {
			return mappings, nil
		},
		oiUSDFn: func(ctx context.Context, symbol string) (decimal.Decimal, error) {
			if v, ok := oiByBin[symbol]; ok {
				return v, nil
			}
			return decimal.Zero, errors.New("no oi_history row")
		},
		updateFn: func(ctx context.Context, arg gen.UpdateLatestMarketCapForSymbolParams) error {
			mu.Lock()
			defer mu.Unlock()
			cap.lhRows = append(cap.lhRows, arg)
			return nil
		},
		cacheFn: func(ctx context.Context, arg gen.UpsertCoingeckoMarketCacheParams) error {
			mu.Lock()
			defer mu.Unlock()
			cap.cache = append(cap.cache, arg)
			return nil
		},
	}
	return c, cap
}

func marketsHandler(t *testing.T, body string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/coins/markets" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestSupply_HappyPath_CachesAllSymbols(t *testing.T) {
	c, cap := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "BTCUSDT", CoingeckoID: "bitcoin"},
			{BinanceSymbol: "ETHUSDT", CoingeckoID: "ethereum"},
		},
		map[string]decimal.Decimal{
			"BTCUSDT": decimal.NewFromFloat(15_760_000_000),
			"ETHUSDT": decimal.NewFromFloat(5_040_000_000),
		},
		marketsHandler(t, marketsBatchFixture),
	)
	require.NoError(t, c.Run(context.Background()))
	// R.12.B: cache row for every mapped symbol (full-market path).
	require.Len(t, cap.cache, 2)
	gotCache := map[string]gen.UpsertCoingeckoMarketCacheParams{}
	for _, p := range cap.cache {
		gotCache[p.BinanceSymbol] = p
	}
	assert.True(t, gotCache["BTCUSDT"].CirculatingSupply.Decimal.Equal(decimal.NewFromInt(19_700_000)))
	assert.True(t, gotCache["BTCUSDT"].MarketCapUsd.Valid)
	// Legacy lh path: both BTC + ETH had oi_history rows → both lh updated.
	require.Len(t, cap.lhRows, 2)
	one := decimal.NewFromInt(1)
	gotLh := map[string]gen.UpdateLatestMarketCapForSymbolParams{}
	for _, p := range cap.lhRows {
		gotLh[p.Symbol] = p
	}
	assert.True(t, gotLh["BTCUSDT"].MarketCapRatioPct.Decimal.Sub(one).Abs().LessThan(decimal.NewFromFloat(0.001)))
}

func TestSupply_NoOIHistory_CachesButSkipsLh(t *testing.T) {
	// R.12.B: even without oi_history (symbol newly listed), cache still
	// receives a row — Market 页 cmcap doesn't need OI.
	c, cap := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "BTCUSDT", CoingeckoID: "bitcoin"},
			{BinanceSymbol: "ETHUSDT", CoingeckoID: "ethereum"},
		},
		map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromFloat(15_760_000_000)}, // ETH no OI
		marketsHandler(t, marketsBatchFixture),
	)
	require.NoError(t, c.Run(context.Background()))
	assert.Len(t, cap.cache, 2, "both symbols cached regardless of OI presence")
	assert.Len(t, cap.lhRows, 1, "only BTC had OI → only BTC legacy lh update")
}

func TestSupply_MarketCapAndSupplyZero_SkipsSymbol(t *testing.T) {
	c, cap := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "RUGUSDT", CoingeckoID: "rugcoin"},
		},
		map[string]decimal.Decimal{"RUGUSDT": decimal.NewFromInt(1000)},
		marketsHandler(t, `[{"id":"rugcoin","symbol":"rug","current_price":0,"market_cap":0,"circulating_supply":0}]`),
	)
	require.NoError(t, c.Run(context.Background()))
	assert.Empty(t, cap.cache, "all 0 → noise row, skip")
	assert.Empty(t, cap.lhRows)
}

func TestSupply_EmptyMappings_Error(t *testing.T) {
	c, _ := newTestSupplyCollector(t, nil, nil, marketsHandler(t, "[]"))
	require.Error(t, c.Run(context.Background()))
}

func TestSupply_BatchEndpointFails_NoCacheWrites(t *testing.T) {
	c, cap := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "BTCUSDT", CoingeckoID: "bitcoin"},
		},
		map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(1000)},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}),
	)
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0 markets")
	assert.Empty(t, cap.cache)
	assert.Empty(t, cap.lhRows)
}

func TestSupply_CoinGeckoMissingForOneSymbol_PartialCache(t *testing.T) {
	c, cap := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "BTCUSDT", CoingeckoID: "bitcoin"},
			{BinanceSymbol: "ETHUSDT", CoingeckoID: "ethereum"},
		},
		map[string]decimal.Decimal{
			"BTCUSDT": decimal.NewFromFloat(15_760_000_000),
			"ETHUSDT": decimal.NewFromFloat(5_040_000_000),
		},
		marketsHandler(t, `[{"id":"bitcoin","symbol":"btc","current_price":80000,"market_cap":1576000000000,"circulating_supply":19700000}]`),
	)
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, cap.cache, 1)
	assert.Equal(t, "BTCUSDT", cap.cache[0].BinanceSymbol)
}
