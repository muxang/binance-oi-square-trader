package binance

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const symbolServiceFixture = `{
  "symbols": [
    {"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"ETHUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
    {"symbol":"SOLUSDT","contractType":"PERPETUAL","status":"TRADING","quoteAsset":"USDT","marginAsset":"USDT"},
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
