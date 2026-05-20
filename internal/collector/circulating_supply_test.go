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

func newTestSupplyCollector(
	t *testing.T,
	mappings []gen.ListCoingeckoMappingsRow,
	oiByBin map[string]decimal.Decimal,
	h http.Handler,
) (*CirculatingSupplyCollector, *[]gen.UpdateLatestMarketCapForSymbolParams) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	mu := sync.Mutex{}
	captured := []gen.UpdateLatestMarketCapForSymbolParams{}

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
			captured = append(captured, arg)
			return nil
		},
	}
	return c, &captured
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

func TestSupply_HappyPath_ComputesRatio(t *testing.T) {
	// BTC: OI_USD=15.76 B → ratio = 15.76B / 1576B × 100 = 1.0%
	// ETH: OI_USD=5.04 B → ratio = 5.04B / 504B × 100 = 1.0%
	c, captured := newTestSupplyCollector(t,
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
	require.Len(t, *captured, 2)

	got := map[string]gen.UpdateLatestMarketCapForSymbolParams{}
	for _, p := range *captured {
		got[p.Symbol] = p
	}
	// Both ratios should equal 1.0% (within decimal precision).
	one := decimal.NewFromInt(1)
	assert.True(t, got["BTCUSDT"].MarketCapRatioPct.Decimal.Sub(one).Abs().LessThan(decimal.NewFromFloat(0.001)),
		"BTC ratio expected ~1%%, got %s", got["BTCUSDT"].MarketCapRatioPct.Decimal)
	assert.True(t, got["ETHUSDT"].MarketCapRatioPct.Decimal.Sub(one).Abs().LessThan(decimal.NewFromFloat(0.001)),
		"ETH ratio expected ~1%%, got %s", got["ETHUSDT"].MarketCapRatioPct.Decimal)
	assert.True(t, got["BTCUSDT"].CirculatingSupply.Decimal.Equal(decimal.NewFromInt(19_700_000)))
}

func TestSupply_NoOIHistory_SkipsSymbol(t *testing.T) {
	c, captured := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "BTCUSDT", CoingeckoID: "bitcoin"},
			{BinanceSymbol: "ETHUSDT", CoingeckoID: "ethereum"},
		},
		map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromFloat(15_760_000_000)}, // ETH absent
		marketsHandler(t, marketsBatchFixture),
	)
	require.NoError(t, c.Run(context.Background()))
	assert.Len(t, *captured, 1)
	assert.Equal(t, "BTCUSDT", (*captured)[0].Symbol)
}

func TestSupply_MarketCapZero_SkipsSymbol(t *testing.T) {
	c, captured := newTestSupplyCollector(t,
		[]gen.ListCoingeckoMappingsRow{
			{BinanceSymbol: "RUGUSDT", CoingeckoID: "rugcoin"},
		},
		map[string]decimal.Decimal{"RUGUSDT": decimal.NewFromInt(1000)},
		marketsHandler(t, `[{"id":"rugcoin","symbol":"rug","current_price":0,"market_cap":0,"circulating_supply":0}]`),
	)
	require.NoError(t, c.Run(context.Background()))
	assert.Empty(t, *captured, "market_cap=0 → skip (division-by-zero guard)")
}

func TestSupply_EmptyMappings_Error(t *testing.T) {
	c, _ := newTestSupplyCollector(t, nil, nil, marketsHandler(t, "[]"))
	require.Error(t, c.Run(context.Background()))
}

func TestSupply_BatchEndpointFails_PartialDataNoError(t *testing.T) {
	// Single batch fails entirely → markets map empty → returns specific error.
	c, captured := newTestSupplyCollector(t,
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
	assert.Empty(t, *captured)
}

func TestSupply_CoinGeckoMissingForOneSymbol_SkipsRow(t *testing.T) {
	// /coins/markets returns only bitcoin; ETH mapping has no market data → skip.
	c, captured := newTestSupplyCollector(t,
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
	require.Len(t, *captured, 1)
	assert.Equal(t, "BTCUSDT", (*captured)[0].Symbol)
}
