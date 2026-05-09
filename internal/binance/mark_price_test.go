package binance

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const markPriceFixture = `{"symbol":"BTCUSDT","markPrice":"80300.50000000","indexPrice":"80299.12345678","estimatedSettlePrice":"80300.0","lastFundingRate":"0.00010000","interestRate":"0.00010000","nextFundingTime":1700000000000,"time":1699999940000}`

func TestFetchMarkPrice_ParsesValidResponse(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = markPriceFixture
	c := mustNewTestnet(t, fp)
	data, err := c.FetchMarkPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, "BTCUSDT", data.Symbol)
	assert.True(t, data.MarkPrice.Equal(decimal.RequireFromString("80300.50000000")))

	// query param symbol=BTCUSDT must be on the wire.
	fp.rec.mu.Lock()
	defer fp.rec.mu.Unlock()
	require.Len(t, fp.rec.requests, 1)
	assert.Equal(t, "BTCUSDT", fp.rec.requests[0].URL.Query().Get("symbol"))
}

// TestFetchMarkPrice_DecimalPrecision uses 18-digit fractional value —
// float64 (~15 sig digits) would round-trip away.
func TestFetchMarkPrice_DecimalPrecision(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = `{"symbol":"BTCUSDT","markPrice":"80123.456789012345678"}`
	c := mustNewTestnet(t, fp)
	data, err := c.FetchMarkPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	want := decimal.RequireFromString("80123.456789012345678")
	assert.True(t, data.MarkPrice.Equal(want), "got %s, want %s", data.MarkPrice, want)
}

func TestFetchMarkPrice_HTTPError_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.status = 500
	fp.rec.respBody = `{"code":-1,"msg":"server boom"}`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchMarkPrice(context.Background(), "BTCUSDT")
	require.Error(t, err)
}

func TestFetchMarkPrice_MalformedJSON_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = "not json"
	c := mustNewTestnet(t, fp)
	_, err := c.FetchMarkPrice(context.Background(), "BTCUSDT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// BAPI normally returns markPrice as string. If a future API change ever
// returns a number, strict typing must reject — never silently coerce.
func TestFetchMarkPrice_MarkPriceNotString_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = `{"symbol":"BTCUSDT","markPrice":80300}`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchMarkPrice(context.Background(), "BTCUSDT")
	require.Error(t, err)
}
