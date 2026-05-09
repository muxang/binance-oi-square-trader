package binance

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKlines_ValidResponse(t *testing.T) {
	raw := []byte(`[
[1700000000000, "80000.00", "80100.00", "79900.00", "80050.00", "100.5", 1700000299999, "8050000.00", 1234, "50.0", "4000000.00", "0"],
[1700000300000, "80050.00", "80200.00", "79800.00", "80100.00", "200.0", 1700000599999, "16000000.00", 5678, "100.0", "8000000.00", "0"]
]`)
	bars, err := ParseKlines(raw)
	require.NoError(t, err)
	require.Len(t, bars, 2)

	assert.Equal(t, time.UnixMilli(1700000000000).UTC(), bars[0].OpenTime)
	assert.True(t, bars[0].Open.Equal(decimal.RequireFromString("80000.00")))
	assert.True(t, bars[0].High.Equal(decimal.RequireFromString("80100.00")))
	assert.True(t, bars[0].Low.Equal(decimal.RequireFromString("79900.00")))
	assert.True(t, bars[0].Close.Equal(decimal.RequireFromString("80050.00")))
	assert.True(t, bars[0].Volume.Equal(decimal.RequireFromString("100.5")))
	// row[7] — quote_asset_volume (USDT-denominated). T7 / Phase 2 use this.
	assert.True(t, bars[0].QuoteVolume.Equal(decimal.RequireFromString("8050000.00")))

	assert.Equal(t, time.UnixMilli(1700000300000).UTC(), bars[1].OpenTime)
	assert.True(t, bars[1].Close.Equal(decimal.RequireFromString("80100.00")))
	assert.True(t, bars[1].QuoteVolume.Equal(decimal.RequireFromString("16000000.00")))
}

// TestParseKlines_DecimalPrecision uses values float64 cannot represent
// exactly. If anywhere on the parse path drops to float64, these asserts fail.
func TestParseKlines_DecimalPrecision(t *testing.T) {
	raw := []byte(`[[1700000000000, "80123.456789012345", "80200.000000000001", "80000.999999999999", "80100.123456789012", "100.123456789012345", 1700000299999, "0", 0, "0", "0", "0"]]`)
	bars, err := ParseKlines(raw)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.True(t, bars[0].Open.Equal(decimal.RequireFromString("80123.456789012345")), "Open precision lost: %s", bars[0].Open)
	assert.True(t, bars[0].High.Equal(decimal.RequireFromString("80200.000000000001")), "High precision lost: %s", bars[0].High)
	assert.True(t, bars[0].Low.Equal(decimal.RequireFromString("80000.999999999999")), "Low precision lost: %s", bars[0].Low)
	assert.True(t, bars[0].Close.Equal(decimal.RequireFromString("80100.123456789012")), "Close precision lost: %s", bars[0].Close)
	assert.True(t, bars[0].Volume.Equal(decimal.RequireFromString("100.123456789012345")), "Volume precision lost: %s", bars[0].Volume)
}

func TestParseKlines_TimestampUTC(t *testing.T) {
	raw := []byte(`[[1700000000000, "1", "1", "1", "1", "0", 1700000299999, "0", 0, "0", "0", "0"]]`)
	bars, err := ParseKlines(raw)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.Equal(t, time.UTC, bars[0].OpenTime.Location())
	assert.True(t, bars[0].OpenTime.Equal(time.UnixMilli(1700000000000).UTC()))
}

func TestParseKlines_EmptyArray(t *testing.T) {
	bars, err := ParseKlines([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bars)
}

func TestParseKlines_MalformedRow_NotString(t *testing.T) {
	// row[1] (open) is a JSON number not a string — parser must error.
	raw := []byte(`[[1700000000000, 80000, "80100", "79900", "80050", "100", 1700000299999, "0", 0, "0", "0", "0"]]`)
	_, err := ParseKlines(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

func TestParseKlines_TimestampNotNumber(t *testing.T) {
	// row[0] (open_time) is a string not a number — parser must error.
	raw := []byte(`[["1700000000000", "1", "1", "1", "1", "0", 1700000299999, "0", 0, "0", "0", "0"]]`)
	_, err := ParseKlines(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open_time")
}
