package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/config"
)

// rewritingTransport routes every request to a fixed test-server URL so we
// can stand up the binance.Client against an httptest backend without
// touching the binance package's private base-URL fields.
type rewritingTransport struct{ target *url.URL }

func (r *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

type fakeProxy struct{ target *url.URL }

func (f *fakeProxy) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return &http.Client{Transport: &rewritingTransport{target: f.target}}, "fake://proxy", nil
}
func (f *fakeProxy) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	return nil, "", errors.New("not used")
}
func (f *fakeProxy) ReportFailure(string, error) {}
func (f *fakeProxy) ReportSuccess(string)        {}
func (f *fakeProxy) Stats() binance.ProxyStats   { return binance.ProxyStats{Mode: "fake"} }

const exchangeInfoFixture = `{
  "symbols": [
    {"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"ETHUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"SOLUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"BTCUSD_PERP","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USD","marginAsset":"BTC"},
    {"symbol":"ETHUSDT_240329","contractType":"CURRENT_QUARTER","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"OLDUSDT","contractType":"PERPETUAL","status":"SETTLING","quoteAsset":"USDT","marginAsset":"USDT"}
  ]
}`

func oiHistFixture(symbol string) string {
	return fmt.Sprintf(`[
  {"symbol":"%s","sumOpenInterest":"12345.6789012345","sumOpenInterestValue":"500000000.12345678","timestamp":1700000000000},
  {"symbol":"%s","sumOpenInterest":"12400.0000000000","sumOpenInterestValue":"500200000.00000000","timestamp":1700000300000},
  {"symbol":"%s","sumOpenInterest":"12500.0000000000","sumOpenInterestValue":"500500000.00000000","timestamp":1700000600000}
]`, symbol, symbol, symbol)
}

// newServer wires a httptest server with default fixtures plus per-symbol
// failure injection (key=symbol, value=http status code).
func newServer(t *testing.T, fail map[string]int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	exiCalls := &atomic.Int32{}
	ohCalls := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/exchangeInfo", func(w http.ResponseWriter, _ *http.Request) {
		exiCalls.Add(1)
		_, _ = w.Write([]byte(exchangeInfoFixture))
	})
	mux.HandleFunc("/futures/data/openInterestHist", func(w http.ResponseWriter, r *http.Request) {
		ohCalls.Add(1)
		sym := r.URL.Query().Get("symbol")
		if code, ok := fail[sym]; ok {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol"}`))
			return
		}
		_, _ = w.Write([]byte(oiHistFixture(sym)))
	})
	return httptest.NewServer(mux), exiCalls, ohCalls
}

// testCollector builds an OICollector wired to the given fake server. writeFn
// is captured into wrote — tests can assert what was passed to writeBatch.
func testCollector(t *testing.T, server *httptest.Server, cfg OICollectorConfig) (*OICollector, *[]OIPoint) {
	t.Helper()
	target, _ := url.Parse(server.URL)
	cfgC := &config.Config{
		Mode:    "testnet",
		Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"},
	}
	client, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}
	if cfg.SymbolCacheTTL == 0 {
		cfg.SymbolCacheTTL = time.Hour
	}
	if cfg.OIHistLimit == 0 {
		cfg.OIHistLimit = 3
	}
	if cfg.HighFailureRate == 0 {
		cfg.HighFailureRate = 0.50
	}
	wrote := &[]OIPoint{}
	c := &OICollector{
		client:  client,
		log:     zerolog.Nop(),
		cfg:     cfg,
		nowFunc: time.Now,
		writeFn: func(_ context.Context, points []OIPoint) (int, error) {
			*wrote = append(*wrote, points...)
			return len(points), nil
		},
	}
	return c, wrote
}

// --- tests --------------------------------------------------------------

func TestNewOICollector_AppliesDefaults(t *testing.T) {
	cfgC := &config.Config{
		Mode:    "testnet",
		Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"},
	}
	target, _ := url.Parse("http://example.invalid")
	client, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)
	c := NewOICollector(client, nil, zerolog.Nop(), OICollectorConfig{})
	assert.Equal(t, 8, c.cfg.Concurrency)
	assert.Equal(t, time.Hour, c.cfg.SymbolCacheTTL)
	assert.Equal(t, 10, c.cfg.OIHistLimit)
	assert.InDelta(t, 0.30, c.cfg.HighFailureRate, 1e-9)
	assert.Equal(t, "oi", c.Name())
}

func TestFetchSymbols_FiltersToUSDTPerpetualsTrading(t *testing.T) {
	server, _, _ := newServer(t, nil)
	defer server.Close()
	c, _ := testCollector(t, server, OICollectorConfig{})
	got, err := c.fetchSymbols(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, got,
		"must keep only PERPETUAL + USDT quote/margin + TRADING")
}

func TestFetchSymbols_UsesCache_WhenFresh(t *testing.T) {
	server, exiCalls, _ := newServer(t, nil)
	defer server.Close()
	c, _ := testCollector(t, server, OICollectorConfig{SymbolCacheTTL: time.Hour})
	_, err := c.fetchSymbols(context.Background())
	require.NoError(t, err)
	_, err = c.fetchSymbols(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, exiCalls.Load(), "second call within TTL must not re-fetch")
}

func TestFetchSymbols_RefreshesCache_WhenExpired(t *testing.T) {
	server, exiCalls, _ := newServer(t, nil)
	defer server.Close()
	c, _ := testCollector(t, server, OICollectorConfig{SymbolCacheTTL: time.Hour})
	_, err := c.fetchSymbols(context.Background())
	require.NoError(t, err)
	// Force expiry by rolling cachedAt back.
	c.symbolsMu.Lock()
	c.symbolsAt = time.Now().Add(-2 * time.Hour)
	c.symbolsMu.Unlock()
	_, err = c.fetchSymbols(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, exiCalls.Load(), "expired cache must re-fetch")
}

func TestFetchSingleOI_ParsesDecimalAndTimestamp(t *testing.T) {
	server, _, _ := newServer(t, nil)
	defer server.Close()
	c, _ := testCollector(t, server, OICollectorConfig{})
	pts, err := c.fetchSingleOI(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	require.Len(t, pts, 3)

	// Decimal values must round-trip exactly — no float64 truncation.
	assert.True(t, pts[0].OI.Equal(decimal.RequireFromString("12345.6789012345")), "OI exact decimal")
	assert.True(t, pts[0].OIValueUSD.Equal(decimal.RequireFromString("500000000.12345678")), "OIValueUSD exact decimal")

	// Timestamp must be UTC, ms-precise, and from the server (1700000000000 ms = 2023-11-14 22:13:20 UTC).
	expected := time.UnixMilli(1700000000000).UTC()
	assert.True(t, pts[0].TS.Equal(expected), "got %v want %v", pts[0].TS, expected)
	assert.Equal(t, time.UTC, pts[0].TS.Location())

	// 5-min boundaries: subsequent timestamps differ by exactly 5min.
	assert.Equal(t, 5*time.Minute, pts[1].TS.Sub(pts[0].TS))
	assert.Equal(t, 5*time.Minute, pts[2].TS.Sub(pts[1].TS))
}

func TestRun_ConcurrentFetch_RespectsLimit(t *testing.T) {
	// Make every openInterestHist handler block until released; count peak
	// concurrency. With Concurrency=2 and 3 symbols, peak must be ≤ 2.
	var inflight, peak atomic.Int32
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/exchangeInfo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(exchangeInfoFixture))
	})
	mux.HandleFunc("/futures/data/openInterestHist", func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inflight.Add(-1)
		_, _ = w.Write([]byte(oiHistFixture(r.URL.Query().Get("symbol"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c, _ := testCollector(t, server, OICollectorConfig{Concurrency: 2})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	require.NoError(t, <-done)
	assert.LessOrEqual(t, peak.Load(), int32(2), "peak in-flight requests must respect Concurrency=2")
}

func TestRun_PartialFailure_ContinuesOthers(t *testing.T) {
	// BTCUSDT returns 400; ETHUSDT and SOLUSDT succeed. Run must succeed and
	// write 2 symbols' worth of points.
	server, _, _ := newServer(t, map[string]int{"BTCUSDT": 400})
	defer server.Close()
	c, wrote := testCollector(t, server, OICollectorConfig{HighFailureRate: 0.99})
	require.NoError(t, c.Run(context.Background()))
	// 3 fixture rows × 2 successful symbols = 6 points.
	assert.Len(t, *wrote, 6, "must persist points from non-failing symbols")
}

func TestRun_AllSymbolsFailed_ReturnsError(t *testing.T) {
	server, _, _ := newServer(t, map[string]int{
		"BTCUSDT": 500, "ETHUSDT": 500, "SOLUSDT": 500,
	})
	defer server.Close()
	c, wrote := testCollector(t, server, OICollectorConfig{HighFailureRate: 0.99})
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full-tick failure")
	assert.Empty(t, *wrote)
}

func TestRun_DBWriteFailure_DoesNotPanic(t *testing.T) {
	server, _, _ := newServer(t, nil)
	defer server.Close()
	c, _ := testCollector(t, server, OICollectorConfig{})
	c.writeFn = func(_ context.Context, _ []OIPoint) (int, error) { return 0, errors.New("db boom") }
	require.NotPanics(t, func() {
		_ = c.Run(context.Background())
	})
}
