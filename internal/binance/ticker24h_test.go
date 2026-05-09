package binance

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ticker24hFixture = `[
{"symbol":"BTCUSDT","quoteVolume":"12345678901.234567890"},
{"symbol":"ETHUSDT","quoteVolume":"2345678901.123456789"},
{"symbol":"SOLUSDT","quoteVolume":"234567890.987654321"}
]`

func TestFetchAll24hTicker_ParsesValidResponse(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = ticker24hFixture
	c := mustNewTestnet(t, fp)
	data, err := c.FetchAll24hTicker(context.Background())
	require.NoError(t, err)
	require.Len(t, data, 3)
	assert.Equal(t, "BTCUSDT", data[0].Symbol)
	assert.True(t, data[0].QuoteVolume.Equal(decimal.RequireFromString("12345678901.234567890")))
	assert.Equal(t, "ETHUSDT", data[1].Symbol)
	assert.True(t, data[1].QuoteVolume.Equal(decimal.RequireFromString("2345678901.123456789")))
	assert.Equal(t, "SOLUSDT", data[2].Symbol)
}

// TestFetchAll24hTicker_DecimalPrecision uses an 18-digit fractional value
// — float64 (~15 sig digits) would round-trip away.
func TestFetchAll24hTicker_DecimalPrecision(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = `[{"symbol":"BTCUSDT","quoteVolume":"12345678901.234567890123456789"}]`
	c := mustNewTestnet(t, fp)
	data, err := c.FetchAll24hTicker(context.Background())
	require.NoError(t, err)
	require.Len(t, data, 1)
	want := decimal.RequireFromString("12345678901.234567890123456789")
	assert.True(t, data[0].QuoteVolume.Equal(want), "got %s, want %s", data[0].QuoteVolume, want)
}

func TestFetchAll24hTicker_HTTPError_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.status = 500
	fp.rec.respBody = `{"code":-1,"msg":"server boom"}`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchAll24hTicker(context.Background())
	require.Error(t, err)
}

func TestFetchAll24hTicker_MalformedJSON_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = "not json"
	c := mustNewTestnet(t, fp)
	_, err := c.FetchAll24hTicker(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// BAPI normally returns quoteVolume as string. If a future API change ever
// returns a number, strict typing must reject — never silently coerce.
func TestFetchAll24hTicker_QuoteVolumeNotString_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = `[{"symbol":"BTCUSDT","quoteVolume":12345}]`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchAll24hTicker(context.Background())
	require.Error(t, err)
}
