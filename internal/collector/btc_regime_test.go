package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/config"
)

// klinesFixture composes a 2-bar /fapi/v1/klines response. Each bar mirrors
// binance's heterogeneous tuple shape: [openTimeMs, "open", "high", "low",
// "close", "volume", closeTimeMs, "quoteVol", trades, "tbBase", "tbQuote",
// "ignore"].
func klinesFixture(prevClose, openStr, highStr, lowStr, closeStr string) string {
	openTime := int64(1700000300000)
	closeTime := int64(1700000599999)
	return `[` +
		`[1700000000000, "` + prevClose + `", "` + prevClose + `", "` + prevClose + `", "` + prevClose + `", "100", 1700000299999, "8000000", 1234, "50", "4000000", "0"],` +
		`[` + jsonNum(openTime) + `, "` + openStr + `", "` + highStr + `", "` + lowStr + `", "` + closeStr + `", "200", ` + jsonNum(closeTime) + `, "16000000", 5678, "100", "8000000", "0"]` +
		`]`
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

// newKlinesServer mounts a stub /fapi/v1/klines that returns the given body.
// If statusCode != 0 the handler returns that status with an empty body.
func newKlinesServer(t *testing.T, body string, statusCode int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, _ *http.Request) {
		if statusCode != 0 {
			w.WriteHeader(statusCode)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// newBTCRegime wires a BTCRegimeCollector against a fake binance + miniredis.
// Returns the collector, its miniredis handle (for direct inspection), and
// its redis.Client (in case the caller wants to close it for error tests).
func newBTCRegime(t *testing.T, server *httptest.Server) (*BTCRegimeCollector, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	target, _ := url.Parse(server.URL)
	cfgC := &config.Config{
		Mode:    "testnet",
		Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"},
	}
	client, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)
	c := NewBTCRegimeCollector(client, rdb, zerolog.Nop(), BTCRegimeConfig{})
	return c, mr, rdb
}

// readData parses the JSON stored at the BTC regime key.
func readData(t *testing.T, mr *miniredis.Miniredis, key string) BTCRegimeData {
	t.Helper()
	raw, err := mr.Get(key)
	require.NoError(t, err)
	var d BTCRegimeData
	require.NoError(t, json.Unmarshal([]byte(raw), &d))
	return d
}

func TestNewBTCRegimeCollector_Defaults(t *testing.T) {
	c := NewBTCRegimeCollector(nil, nil, zerolog.Nop(), BTCRegimeConfig{})
	assert.Equal(t, "btc_5m_change", c.cfg.RedisKey)
	assert.Equal(t, 5*time.Minute, c.cfg.RedisTTL)
	assert.Equal(t, "btc_regime", c.Name())
}

func TestRun_DropPct_Positive_OnDecline(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "80100", "77600", "77600"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	d := readData(t, mr, "btc_5m_change")
	// (80000 - 77600) / 80000 = 0.03
	assert.True(t, d.DropPct.Equal(decimal.RequireFromString("0.03")), "got %s", d.DropPct)
}

func TestRun_DropPct_Negative_OnRise(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "82500", "79900", "82400"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	d := readData(t, mr, "btc_5m_change")
	// (80000 - 82400) / 80000 = -0.03  (negative = rise; never abs())
	assert.True(t, d.DropPct.Equal(decimal.RequireFromString("-0.03")), "got %s", d.DropPct)
	assert.True(t, d.DropPct.IsNegative(), "rise must produce negative drop_pct")
}

func TestRun_DropPct_Zero_OnFlat(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "80100", "79900", "80000"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	d := readData(t, mr, "btc_5m_change")
	assert.True(t, d.DropPct.IsZero(), "got %s", d.DropPct)
}

func TestRun_DropPct_DecimalPrecision(t *testing.T) {
	// Pick values float64 cannot represent exactly — assert exact match.
	srv := newKlinesServer(t, klinesFixture("80000", "80123.456789012345", "80200", "80000", "80100.123456789012"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	d := readData(t, mr, "btc_5m_change")
	expected := decimal.RequireFromString("80123.456789012345").Sub(decimal.RequireFromString("80100.123456789012")).
		Div(decimal.RequireFromString("80123.456789012345"))
	assert.True(t, d.DropPct.Equal(expected), "want %s got %s", expected, d.DropPct)
	assert.True(t, d.Open.Equal(decimal.RequireFromString("80123.456789012345")), "open precision lost")
	assert.True(t, d.Close.Equal(decimal.RequireFromString("80100.123456789012")), "close precision lost")
}

func TestRun_RedisWrite_ContainsAllFields(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "80100", "79900", "78400"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	d := readData(t, mr, "btc_5m_change")
	assert.False(t, d.DropPct.IsZero())
	assert.True(t, d.Open.Equal(decimal.RequireFromString("80000")))
	assert.True(t, d.Close.Equal(decimal.RequireFromString("78400")))
	expectedOpenTime := time.UnixMilli(1700000300000).UTC()
	assert.True(t, d.OpenTime.Equal(expectedOpenTime), "open_time mismatch")
	assert.False(t, d.CheckedAt.IsZero())
	assert.Equal(t, time.UTC, d.OpenTime.Location())
}

func TestRun_RedisTTL_5Minutes(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "80100", "79900", "78400"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.NoError(t, c.Run(context.Background()))
	ttl := mr.TTL("btc_5m_change")
	assert.Equal(t, 5*time.Minute, ttl, "TTL must be 5 minutes")
}

func TestRun_KlinesFetchError_ReturnsError(t *testing.T) {
	srv := newKlinesServer(t, "", http.StatusInternalServerError)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	require.Error(t, c.Run(context.Background()))
	_, err := mr.Get("btc_5m_change")
	assert.Error(t, err, "redis must NOT be written when klines fetch fails")
}

func TestRun_RedisWriteError_ReturnsError(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "80000", "80100", "79900", "78400"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	mr.Close() // simulate redis outage
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis set")
}

func TestRun_OpenZero_SkipsAndLogsError(t *testing.T) {
	srv := newKlinesServer(t, klinesFixture("80000", "0", "0", "0", "78400"), 0)
	defer srv.Close()
	c, mr, _ := newBTCRegime(t, srv)
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open=0")
	_, redisErr := mr.Get("btc_5m_change")
	assert.Error(t, redisErr, "redis must NOT be written when open=0")
}
