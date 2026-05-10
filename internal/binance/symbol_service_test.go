package binance

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const symbolServiceFixture = `{
  "symbols": [
    {"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"MIN_NOTIONAL","notional":"5"}]},
    {"symbol":"ETHUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"SOLUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.01","minQty":"0.1"}]},
    {"symbol":"BTCUSD_PERP","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USD","marginAsset":"BTC"},
    {"symbol":"ETHUSDT_240329","contractType":"CURRENT_QUARTER","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"OLDUSDT","contractType":"PERPETUAL","status":"SETTLING","quoteAsset":"USDT","marginAsset":"USDT"}
  ]
}`

func newSymbolServiceTest(t *testing.T) (*SymbolService, *fakeProxy) {
	t.Helper()
	fp := newFakeProxy()
	fp.rec.respBody = symbolServiceFixture
	c := mustNewTestnet(t, fp)
	return NewSymbolService(c, zerolog.Nop()), fp
}

func TestSymbolService_IsValidPerpetual_True(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	ok, err := s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSymbolService_IsValidPerpetual_False_ForCoinMargined(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	// BTCUSD_PERP is USD-quoted (coin-margined), not USDT — must be filtered out.
	ok, err := s.IsValidPerpetual(context.Background(), "BTCUSD_PERP")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSymbolService_IsValidPerpetual_False_ForUnknown(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	ok, err := s.IsValidPerpetual(context.Background(), "AAPL")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSymbolService_ListSymbols_FiltersToActiveUSDTPerps(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	syms, err := s.ListSymbols(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, syms)
}

func TestSymbolService_ListSymbols_ReturnsCopy(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	syms, err := s.ListSymbols(context.Background())
	require.NoError(t, err)
	syms[0] = "MUTATED"
	syms2, err := s.ListSymbols(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, syms2, "MUTATED", "internal cache must not share backing array with returned slice")
}

func TestSymbolService_UsesCache(t *testing.T) {
	s, fp := newSymbolServiceTest(t)
	_, err := s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	_, err = s.IsValidPerpetual(context.Background(), "ETHUSDT")
	require.NoError(t, err)
	fp.rec.mu.Lock()
	n := len(fp.rec.requests)
	fp.rec.mu.Unlock()
	assert.Equal(t, 1, n, "second call within TTL must hit cache")
}

func TestSymbolService_RefreshesCache_WhenExpired(t *testing.T) {
	s, fp := newSymbolServiceTest(t)
	now := time.Now()
	s.nowFunc = func() time.Time { return now }
	_, err := s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	s.nowFunc = func() time.Time { return now.Add(2 * time.Hour) }
	_, err = s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	fp.rec.mu.Lock()
	n := len(fp.rec.requests)
	fp.rec.mu.Unlock()
	assert.Equal(t, 2, n, "expired cache (>1h) must re-fetch")
}

func TestSymbolService_FetchError_BubblesUp(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.status = 500
	fp.rec.respBody = `{"code":-1,"msg":"server boom"}`
	s := NewSymbolService(mustNewTestnet(t, fp), zerolog.Nop())
	_, err := s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.Error(t, err)
}

func TestSymbolService_ParseError_BubblesUp(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = "not json {"
	s := NewSymbolService(mustNewTestnet(t, fp), zerolog.Nop())
	_, err := s.IsValidPerpetual(context.Background(), "BTCUSDT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// --- GetTradingFilters (Phase 3 sizing) ---

func TestSymbolService_GetTradingFilters_BTCUSDT_AllFields(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	tf, err := s.GetTradingFilters(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.True(t, tf.StepSize.Equal(decimal.NewFromFloat(0.001)), "stepSize=0.001, got %s", tf.StepSize)
	assert.True(t, tf.MinQty.Equal(decimal.NewFromFloat(0.001)), "minQty=0.001, got %s", tf.MinQty)
	assert.True(t, tf.TickSize.Equal(decimal.NewFromFloat(0.10)), "tickSize=0.10, got %s", tf.TickSize)
	assert.True(t, tf.MinNotional.Equal(decimal.NewFromInt(5)), "minNotional=5 (futures `notional` field), got %s", tf.MinNotional)
}

func TestSymbolService_GetTradingFilters_PartialFilters_MissingZero(t *testing.T) {
	// SOLUSDT fixture has only LOT_SIZE — MIN_NOTIONAL / PRICE_FILTER absent → zero.
	s, _ := newSymbolServiceTest(t)
	tf, err := s.GetTradingFilters(context.Background(), "SOLUSDT")
	require.NoError(t, err)
	assert.True(t, tf.StepSize.Equal(decimal.NewFromFloat(0.01)))
	assert.True(t, tf.MinQty.Equal(decimal.NewFromFloat(0.1)))
	assert.True(t, tf.MinNotional.IsZero(), "no MIN_NOTIONAL filter → MinNotional=0 (caller validates)")
	assert.True(t, tf.TickSize.IsZero(), "no PRICE_FILTER → TickSize=0")
}

func TestSymbolService_GetTradingFilters_UnknownSymbol_ReturnsError(t *testing.T) {
	s, _ := newSymbolServiceTest(t)
	_, err := s.GetTradingFilters(context.Background(), "AAPL")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSymbolService_GetTradingFilters_UsesCache(t *testing.T) {
	s, fp := newSymbolServiceTest(t)
	_, err := s.GetTradingFilters(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	_, err = s.GetTradingFilters(context.Background(), "ETHUSDT")
	require.NoError(t, err)
	fp.rec.mu.Lock()
	n := len(fp.rec.requests)
	fp.rec.mu.Unlock()
	assert.Equal(t, 1, n, "GetTradingFilters reuses exchangeInfo cache, no extra API call")
}
