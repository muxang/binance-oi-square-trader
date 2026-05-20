package binance

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real BAPI sample from Top-Long-Short-Account-Ratio docs (2026-05-20 fetched).
// Both account/position endpoints share an identical schema, so one fixture
// covers both.
const accountRatioFixture = `[
  {"symbol":"BTCUSDT","longShortRatio":"1.8105","longAccount":"0.6442","shortAccount":"0.3558","timestamp":"1583139600000"},
  {"symbol":"BTCUSDT","longShortRatio":"0.5576","longAccount":"0.3580","shortAccount":"0.6420","timestamp":"1583139900000"}
]`

func TestFetchTopLongShortAccountRatio_ParsesValid(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = accountRatioFixture
	c := mustNewTestnet(t, fp)
	rows, err := c.FetchTopLongShortAccountRatio(context.Background(), "BTCUSDT", "5m", 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	assert.Equal(t, "BTCUSDT", rows[0].Symbol)
	assert.True(t, rows[0].LongShortRatio.Equal(decimal.RequireFromString("1.8105")))
	assert.True(t, rows[0].LongAccount.Equal(decimal.RequireFromString("0.6442")))
	assert.True(t, rows[0].ShortAccount.Equal(decimal.RequireFromString("0.3558")))
	assert.Equal(t, time.UnixMilli(1583139600000).UTC(), rows[0].Timestamp)

	// query params on wire.
	fp.rec.mu.Lock()
	defer fp.rec.mu.Unlock()
	require.Len(t, fp.rec.requests, 1)
	q := fp.rec.requests[0].URL.Query()
	assert.Equal(t, "BTCUSDT", q.Get("symbol"))
	assert.Equal(t, "5m", q.Get("period"))
	assert.Equal(t, "2", q.Get("limit"))
	assert.Equal(t, "/futures/data/topLongShortAccountRatio", fp.rec.requests[0].URL.Path)
}

// FetchTopLongShortPositionRatio uses the same shape — verify it hits the
// position-ratio path and the response parses identically.
func TestFetchTopLongShortPositionRatio_HitsCorrectPath(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = accountRatioFixture // same schema; reuse fixture
	c := mustNewTestnet(t, fp)
	rows, err := c.FetchTopLongShortPositionRatio(context.Background(), "BTCUSDT", "5m", 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	fp.rec.mu.Lock()
	defer fp.rec.mu.Unlock()
	require.Len(t, fp.rec.requests, 1)
	assert.Equal(t, "/futures/data/topLongShortPositionRatio", fp.rec.requests[0].URL.Path)
	// limit=0 → omit param (Binance default 30)
	assert.Equal(t, "", fp.rec.requests[0].URL.Query().Get("limit"))
}

// BAPI doc says timestamp is STRING; live API returns number. json.Number
// must handle both without erroring.
func TestFetchHolderRatio_TimestampNumberVariant(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = `[{"symbol":"X","longShortRatio":"1.0","longAccount":"0.5","shortAccount":"0.5","timestamp":1583139600000}]`
	c := mustNewTestnet(t, fp)
	rows, err := c.FetchTopLongShortAccountRatio(context.Background(), "X", "5m", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, time.UnixMilli(1583139600000).UTC(), rows[0].Timestamp)
}

func TestFetchHolderRatio_HTTPError(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.status = 500
	fp.rec.respBody = `{"code":-1,"msg":"boom"}`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchTopLongShortAccountRatio(context.Background(), "X", "5m", 1)
	require.Error(t, err)
}

func TestFetchHolderRatio_MalformedJSON(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.respBody = "not json"
	c := mustNewTestnet(t, fp)
	_, err := c.FetchTopLongShortAccountRatio(context.Background(), "X", "5m", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestFetchHolderRatio_RatioNotString_ReturnsError(t *testing.T) {
	fp := newFakeProxy()
	// BAPI MUST return decimal as string. Number variant means upstream change
	// — fail loudly rather than silently coerce to float.
	fp.rec.respBody = `[{"symbol":"X","longShortRatio":1.0,"longAccount":"0.5","shortAccount":"0.5","timestamp":"1583139600000"}]`
	c := mustNewTestnet(t, fp)
	_, err := c.FetchTopLongShortAccountRatio(context.Background(), "X", "5m", 1)
	require.Error(t, err)
}
