package coingecko

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live CoinGecko sample (2026-05-20 fetched). Truncated to 3 coins.
const coinsListFixture = `[
  {"id":"bitcoin","symbol":"btc","name":"Bitcoin"},
  {"id":"ethereum","symbol":"eth","name":"Ethereum"},
  {"id":"binancecoin","symbol":"bnb","name":"BNB"}
]`

const marketsFixture = `[
  {"id":"bitcoin","symbol":"btc","current_price":80000.5,"market_cap":1576000000000,"circulating_supply":19700000},
  {"id":"ethereum","symbol":"eth","current_price":4200,"market_cap":504000000000,"circulating_supply":120000000}
]`

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient("").withCustomBase(srv.URL)
}

func TestListCoins_ParsesValid(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/coins/list", r.URL.Path)
		assert.Equal(t, "false", r.URL.Query().Get("include_platform"))
		_, _ = w.Write([]byte(coinsListFixture))
	}))
	out, err := c.ListCoins(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "bitcoin", out[0].ID)
	assert.Equal(t, "btc", out[0].Symbol)
	assert.Equal(t, "Bitcoin", out[0].Name)
}

func TestGetMarkets_ParsesValid(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/coins/markets", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "usd", q.Get("vs_currency"))
		assert.Equal(t, "bitcoin,ethereum", q.Get("ids"))
		assert.Equal(t, "250", q.Get("per_page"))
		_, _ = w.Write([]byte(marketsFixture))
	}))
	out, err := c.GetMarkets(context.Background(), []string{"bitcoin", "ethereum"}, "usd")
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "bitcoin", out[0].ID)
	assert.InDelta(t, 80000.5, out[0].CurrentPrice, 1e-6)
	assert.InDelta(t, 19700000.0, out[0].CirculatingSupply, 1e-6)
}

func TestGetMarkets_EmptyIDs_NoHTTP(t *testing.T) {
	// Empty ids returns ([],nil) WITHOUT firing an HTTP request — defensive
	// against caller bugs (e.g. empty watchlist).
	called := false
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte("[]"))
	}))
	out, err := c.GetMarkets(context.Background(), nil, "usd")
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.False(t, called, "empty ids should skip HTTP")
}

func TestGetMarkets_BatchTooLarge_ReturnsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not hit network when batch >250")
	}))
	ids := make([]string, 251)
	for i := range ids {
		ids[i] = "x"
	}
	_, err := c.GetMarkets(context.Background(), ids, "usd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ">250")
}

func TestDo_APIKey_QueryParam(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Demo key auth is via query param x_cg_demo_api_key (per references/external/coingecko.md).
		assert.Equal(t, "MY_DEMO_KEY", r.URL.Query().Get("x_cg_demo_api_key"))
		_, _ = w.Write([]byte("[]"))
	}))
	c.apiKey = "MY_DEMO_KEY"
	_, err := c.ListCoins(context.Background())
	require.NoError(t, err)
}

func TestDo_HTTPError_WrapsStatusAndBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"status":{"error_code":429,"error_message":"rate limit"}}`))
	}))
	_, err := c.ListCoins(context.Background())
	require.Error(t, err)
	var he *HTTPError
	require.True(t, errors.As(err, &he), "want HTTPError, got %T", err)
	assert.Equal(t, 429, he.HTTPCode)
	assert.Contains(t, he.Body, "rate limit")
}

func TestDo_LargeErrorBody_Truncated(t *testing.T) {
	huge := strings.Repeat("X", 5000)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(huge))
	}))
	_, err := c.ListCoins(context.Background())
	var he *HTTPError
	require.True(t, errors.As(err, &he))
	assert.LessOrEqual(t, len(he.Body), 520, "body should be truncated to ~500 chars")
	assert.Contains(t, he.Body, "truncated")
}

func TestDo_MalformedJSON_ReturnsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	_, err := c.ListCoins(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestDo_CtxCancel_ReturnsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called — ctx cancelled before request")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListCoins(ctx)
	require.Error(t, err)
}
