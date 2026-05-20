package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/config"
	"trader/internal/storage/postgres/gen"
)

// newTestLargeHolderCollector wires a collector against an in-memory fake
// watchlist + an in-memory capture of upserts. The binance client points at
// httptest server via the same rewritingTransport pattern as oi_test.go.
func newTestLargeHolderCollector(t *testing.T, symbols []string, h http.Handler) (*LargeHolderCollector, *[]gen.InsertLargeHolderRatioParams) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	cfgC := &config.Config{
		Mode:    "testnet",
		Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"},
	}
	bc, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)

	var mu sync.Mutex
	captured := []gen.InsertLargeHolderRatioParams{}
	c := &LargeHolderCollector{
		client: bc,
		log:    zerolog.Nop(),
		cfg:    LargeHolderCollectorConfig{Concurrency: 4, HighFailureRate: 0.30},
		watchlistFn: func(ctx context.Context) ([]string, error) {
			return symbols, nil
		},
		upsertFn: func(ctx context.Context, arg gen.InsertLargeHolderRatioParams) error {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, arg)
			return nil
		},
	}
	return c, &captured
}

const ratioFixture = `[{"symbol":"%s","longShortRatio":"%s","longAccount":"0.6442","shortAccount":"0.3558","timestamp":"1700000000000"}]`

func handlerForRatios(t *testing.T, accountRatio, positionRatio string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sym := r.URL.Query().Get("symbol")
		switch r.URL.Path {
		case "/futures/data/topLongShortAccountRatio":
			_, _ = w.Write([]byte(fmtRatio(sym, accountRatio)))
		case "/futures/data/topLongShortPositionRatio":
			_, _ = w.Write([]byte(fmtRatio(sym, positionRatio)))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func fmtRatio(symbol, ratio string) string {
	return strings.Replace(strings.Replace(ratioFixture, "%s", symbol, 1), "%s", ratio, 1)
}

func TestLargeHolder_HappyPath_UpsertsAllSymbols(t *testing.T) {
	c, captured := newTestLargeHolderCollector(t, []string{"BTCUSDT", "ETHUSDT"},
		handlerForRatios(t, "1.8105", "1.4342"))
	require.NoError(t, c.Run(context.Background()))
	assert.Len(t, *captured, 2)
	got := map[string]gen.InsertLargeHolderRatioParams{}
	for _, p := range *captured {
		got[p.Symbol] = p
	}
	require.Contains(t, got, "BTCUSDT")
	assert.True(t, got["BTCUSDT"].AccountLongShortRatio.Decimal.Equal(decimal.RequireFromString("1.8105")))
	assert.True(t, got["BTCUSDT"].PositionLongShortRatio.Decimal.Equal(decimal.RequireFromString("1.4342")))
}

func TestLargeHolder_EmptyWatchlist_ReturnsError(t *testing.T) {
	c, _ := newTestLargeHolderCollector(t, nil, handlerForRatios(t, "1", "1"))
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty watchlist")
}

// Partial failure: account endpoint 5xx, position endpoint OK → row still written
// with PositionLongShortRatio set, AccountLongShortRatio NULL.
func TestLargeHolder_AccountFails_PositionSucceeds_RowStillWritten(t *testing.T) {
	c, captured := newTestLargeHolderCollector(t, []string{"BTCUSDT"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/futures/data/topLongShortAccountRatio" {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"code":-1,"msg":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(fmtRatio("BTCUSDT", "1.4342")))
		}))
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, *captured, 1)
	row := (*captured)[0]
	assert.False(t, row.AccountLongShortRatio.Valid, "account ratio NULL when account endpoint failed")
	assert.True(t, row.PositionLongShortRatio.Valid)
	assert.True(t, row.PositionLongShortRatio.Decimal.Equal(decimal.RequireFromString("1.4342")))
}

// Both endpoints fail for one symbol → that symbol counted as failure; other
// symbols still upserted.
func TestLargeHolder_BothEndpointsFail_SkipsSymbol(t *testing.T) {
	c, captured := newTestLargeHolderCollector(t, []string{"BAD", "GOOD"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sym := r.URL.Query().Get("symbol")
			if sym == "BAD" {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"code":-1,"msg":"down"}`))
				return
			}
			_, _ = w.Write([]byte(fmtRatio(sym, "1.0")))
		}))
	// 50% failure > 30% threshold → log Warn but no error return (1 symbol succeeded).
	require.NoError(t, c.Run(context.Background()))
	assert.Len(t, *captured, 1)
	assert.Equal(t, "GOOD", (*captured)[0].Symbol)
}

// All symbols fail → Run returns error (full-tick failure).
func TestLargeHolder_AllFail_ReturnsError(t *testing.T) {
	c, captured := newTestLargeHolderCollector(t, []string{"X", "Y"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"code":-1,"msg":"down"}`))
		}))
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full-tick failure")
	assert.Empty(t, *captured)
}

// Watchlist read failure propagates as collector-tick error.
func TestLargeHolder_WatchlistError_PropagatesError(t *testing.T) {
	c, _ := newTestLargeHolderCollector(t, nil, handlerForRatios(t, "1", "1"))
	c.watchlistFn = func(ctx context.Context) ([]string, error) {
		return nil, errors.New("pg down")
	}
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pg down")
}
