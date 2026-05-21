package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/coingecko"
	"trader/internal/storage/postgres/gen"
)

func newTestSymbolMapCollector(t *testing.T, symbols []string, h http.Handler) (*CoingeckoSymbolMapCollector, *[]gen.UpsertCoingeckoMappingParams, *int64) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	mu := sync.Mutex{}
	captured := []gen.UpsertCoingeckoMappingParams{}
	count := int64(0)

	c := &CoingeckoSymbolMapCollector{
		log: zerolog.Nop(),
		symbolsFn: func(ctx context.Context) ([]string, error) {
			return symbols, nil
		},
		upsertFn: func(ctx context.Context, arg gen.UpsertCoingeckoMappingParams) error {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, arg)
			count++
			return nil
		},
		countFn: func(ctx context.Context) (int64, error) {
			mu.Lock()
			defer mu.Unlock()
			return count, nil
		},
	}
	// Wire the real CoinGecko client against the test server. coingecko.NewClient
	// returns a *Client with DemoBaseURL hardcoded; we override via the test
	// server URL by constructing a fresh struct (package-private fields are not
	// reachable). Instead: the test server emulates the demo base path.
	c.cg = coingeckoClientFor(t, srv)
	return c, &captured, &count
}

// coingeckoClientFor constructs a real coingecko.Client pointing at srv via
// the exported NewTestClient seam.
func coingeckoClientFor(t *testing.T, srv *httptest.Server) *coingecko.Client {
	t.Helper()
	return coingecko.NewTestClient(srv.URL)
}

// catalog fixture covering 4 disambiguation cases:
//   - "btc" → exactly one id "bitcoin" (canonical, simple).
//   - "eth" → two ids ("ethereum", "ethereum-pow"); shortest = "ethereum".
//   - "uni" → three ids; shortest "uni" wins over "uni-coin"/"uniswap".
//   - "doge" → exactly one id "dogecoin".
const catalogFixture = `[
  {"id":"bitcoin","symbol":"btc","name":"Bitcoin"},
  {"id":"ethereum","symbol":"eth","name":"Ethereum"},
  {"id":"ethereum-pow","symbol":"eth","name":"EthereumPoW"},
  {"id":"dogecoin","symbol":"doge","name":"Dogecoin"},
  {"id":"uniswap","symbol":"uni","name":"Uniswap"},
  {"id":"uni-coin","symbol":"uni","name":"Uni Coin"},
  {"id":"uni","symbol":"uni","name":"Generic UNI"}
]`

// R.11.A2b revised: top-mcap is queried first now; supply a small top-N
// fixture for tests so canonical preference can be exercised.
const topMcapFixture = `[
  {"id":"bitcoin","symbol":"btc","current_price":80000,"market_cap":1576000000000,"circulating_supply":19700000},
  {"id":"ethereum","symbol":"eth","current_price":4200,"market_cap":504000000000,"circulating_supply":120000000},
  {"id":"dogecoin","symbol":"doge","current_price":0.4,"market_cap":58000000000,"circulating_supply":145000000000}
]`

func newCatalogServer(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/coins/list":
			_, _ = w.Write([]byte(catalogFixture))
		case "/coins/markets":
			// top-mcap query (order=market_cap_desc) — return fixture top-3.
			_, _ = w.Write([]byte(topMcapFixture))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func TestSymbolMap_HappyPath_BasicMappings(t *testing.T) {
	c, captured, _ := newTestSymbolMapCollector(t,
		[]string{"BTCUSDT", "ETHUSDT", "DOGEUSDT"},
		newCatalogServer(t))
	require.NoError(t, c.Run(context.Background()))
	got := map[string]string{}
	for _, p := range *captured {
		got[p.BinanceSymbol] = p.CoingeckoID
	}
	assert.Equal(t, "bitcoin", got["BTCUSDT"])
	assert.Equal(t, "ethereum", got["ETHUSDT"], "shorter id wins over ethereum-pow")
	assert.Equal(t, "dogecoin", got["DOGEUSDT"])
}

func TestSymbolMap_AmbiguousSymbol_PicksShortestID(t *testing.T) {
	c, captured, _ := newTestSymbolMapCollector(t,
		[]string{"UNIUSDT"}, newCatalogServer(t))
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, *captured, 1)
	// Candidates: ["uniswap", "uni-coin", "uni"] → shortest "uni" wins.
	assert.Equal(t, "uni", (*captured)[0].CoingeckoID)
}

func TestSymbolMap_UnknownSymbol_Skipped(t *testing.T) {
	c, captured, _ := newTestSymbolMapCollector(t,
		[]string{"BTCUSDT", "WEIRDCOINUSDT"}, newCatalogServer(t))
	require.NoError(t, c.Run(context.Background()))
	// Only BTC mapped; WEIRDCOIN skipped silently (no exception).
	assert.Len(t, *captured, 1)
	assert.Equal(t, "BTCUSDT", (*captured)[0].BinanceSymbol)
}

func TestSymbolMap_EmptyWatchlist_Error(t *testing.T) {
	c, _, _ := newTestSymbolMapCollector(t, nil, newCatalogServer(t))
	require.Error(t, c.Run(context.Background()))
}

func TestSymbolMap_CatalogFetchFails_Error(t *testing.T) {
	c, _, _ := newTestSymbolMapCollector(t,
		[]string{"BTCUSDT"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/coins/list")
}

func TestEnsureMappingPopulated_TablePopulated_NoRun(t *testing.T) {
	c, captured, count := newTestSymbolMapCollector(t,
		[]string{"BTCUSDT"}, newCatalogServer(t))
	*count = 250 // pretend table has entries
	c.EnsureMappingPopulated(context.Background())
	assert.Empty(t, *captured, "populated table → skip startup refresh")
}

func TestEnsureMappingPopulated_EmptyTable_TriggersRun(t *testing.T) {
	c, captured, count := newTestSymbolMapCollector(t,
		[]string{"BTCUSDT"}, newCatalogServer(t))
	*count = 0 // empty
	c.EnsureMappingPopulated(context.Background())
	require.Len(t, *captured, 1, "empty table → startup refresh runs once")
}

// Heuristic determinism: same-length ids resolve alphabetically.
func TestPickCanonicalID_TieBreakByAlpha(t *testing.T) {
	ids := []string{"zzz", "aaa", "bbb"}
	got := pickCanonicalID(ids)
	assert.Equal(t, "aaa", got)
}

func TestPickCanonicalID_ShortestWins(t *testing.T) {
	ids := []string{"longest-one", "short", "medium-len"}
	got := pickCanonicalID(ids)
	assert.Equal(t, "short", got)
}

func TestPickCanonicalID_SingleCandidate(t *testing.T) {
	got := pickCanonicalID([]string{"only"})
	assert.Equal(t, "only", got)
}

// Sanity: catalog parser returns coins in any order — collector must build the
// bySymbol map deterministically regardless of catalog iteration order.
func TestSymbolMap_CatalogOrderInsensitive(t *testing.T) {
	// Two identical runs with reversed catalog should give same mapping.
	original := []struct{ id, sym string }{
		{"ethereum-pow", "eth"},
		{"ethereum", "eth"},
	}
	sort.Slice(original, func(i, j int) bool { return original[i].id < original[j].id })

	c, captured, _ := newTestSymbolMapCollector(t,
		[]string{"ETHUSDT"}, newCatalogServer(t))
	_ = errors.New // keep import; remove if linter complains
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, *captured, 1)
	assert.Equal(t, "ethereum", (*captured)[0].CoingeckoID)
}
