package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	"trader/internal/storage/postgres/gen"
)

// buildKlinesResp returns a JSON body for /fapi/v1/klines with len(closes)
// bars. Each bar has open=prev_close (or first close), high=close+0.5,
// low=close-0.5, volume=100, quote_volume=10000. Open_time increments by
// 15min from 1700000000000.
//
// Constant-close fixture (e.g. all "100") produces:
//
//	TR_i = max(H-L, |H-prev_C|, |L-prev_C|) = max(1, 0.5, 0.5) = 1   (i >= 1)
//	→ ATR(14) = 1 (constant TR)
//	→ EMA(20) = 100 (constant close)
//
// These are the ground-truth values asserted in TestRun_ComputeATR/EMA.
func buildKlinesResp(closes []string) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, c := range closes {
		if i > 0 {
			sb.WriteByte(',')
		}
		open := c
		if i > 0 {
			open = closes[i-1]
		}
		cd := decimal.RequireFromString(c)
		high := cd.Add(decimal.RequireFromString("0.5")).String()
		low := cd.Sub(decimal.RequireFromString("0.5")).String()
		ot := int64(1700000000000) + int64(i)*15*60*1000
		ct := ot + 15*60*1000 - 1
		fmt.Fprintf(&sb, `[%d,"%s","%s","%s","%s","100",%d,"10000",1234,"50","5000","0"]`, ot, open, high, low, c, ct)
	}
	sb.WriteByte(']')
	return sb.String()
}

func constantCloses(n int, c string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = c
	}
	return out
}

// newT7Server mounts /fapi/v1/exchangeInfo (returns the shared
// exchangeInfoFixture from oi_test.go) and /fapi/v1/klines. fixturesBySymbol
// gives a per-symbol body; missing symbols fall back to 30 constant bars.
// fail returns the given HTTP status for matching symbols.
func newT7Server(t *testing.T, fixturesBySymbol map[string]string, fail map[string]int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	exiCalls := &atomic.Int32{}
	klCalls := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/exchangeInfo", func(w http.ResponseWriter, _ *http.Request) {
		exiCalls.Add(1)
		_, _ = w.Write([]byte(exchangeInfoFixture))
	})
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, r *http.Request) {
		klCalls.Add(1)
		sym := r.URL.Query().Get("symbol")
		if code, ok := fail[sym]; ok {
			w.WriteHeader(code)
			return
		}
		body, ok := fixturesBySymbol[sym]
		if !ok {
			body = buildKlinesResp(constantCloses(30, "100"))
		}
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux), exiCalls, klCalls
}

// writeCapture records every writeFn call so multi-tick tests (upsert) can
// inspect later batches without losing earlier ones.
type writeCapture struct {
	mu    sync.Mutex
	calls [][]gen.BatchUpsertKlinesParams
}

func (w *writeCapture) record(p []gen.BatchUpsertKlinesParams) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, append([]gen.BatchUpsertKlinesParams{}, p...))
}

// testKlinesCollector wires a KlinesCollector against fake binance + miniredis
// + an in-memory writeFn capture. cfg fields can override defaults; missing
// fields fall through klinesDefaults.
func testKlinesCollector(t *testing.T, server *httptest.Server, cfg KlinesCollectorConfig) (*KlinesCollector, *writeCapture, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	target, _ := url.Parse(server.URL)
	cfgC := &config.Config{Mode: "testnet", Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"}}
	client, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg = klinesDefaults(cfg)
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}
	cap := &writeCapture{}
	c := &KlinesCollector{
		client:  client,
		redis:   rdb,
		log:     zerolog.Nop(),
		cfg:     cfg,
		nowFunc: time.Now,
		writeFn: func(_ context.Context, p []gen.BatchUpsertKlinesParams) (int, error) {
			cap.record(p)
			return len(p), nil
		},
	}
	return c, cap, mr, rdb
}

// readPayload parses Redis indicatorPayload at key.
func readPayload(t *testing.T, mr *miniredis.Miniredis, key string) indicatorPayload {
	t.Helper()
	raw, err := mr.Get(key)
	require.NoError(t, err, "key %s missing", key)
	var p indicatorPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return p
}

// --- tests --------------------------------------------------------------

func TestNewKlinesCollector_AppliesDefaults(t *testing.T) {
	cfgC := &config.Config{Mode: "testnet", Binance: config.BinanceConfig{APIKey: "k", APISecret: "s"}}
	target, _ := url.Parse("http://example.invalid")
	client, err := binance.New(cfgC, &fakeProxy{target: target}, binance.NewNoopRateLimiter(), zerolog.Nop())
	require.NoError(t, err)
	c := NewKlinesCollector(client, nil, nil, zerolog.Nop(), KlinesCollectorConfig{})
	assert.Equal(t, 8, c.cfg.Concurrency)
	assert.Equal(t, time.Hour, c.cfg.SymbolCacheTTL)
	assert.Equal(t, 30, c.cfg.KlineLimit)
	assert.Equal(t, "15m", c.cfg.KlineInterval)
	assert.Equal(t, 14, c.cfg.ATRPeriod)
	assert.Equal(t, 20, c.cfg.EMAPeriod)
	assert.Equal(t, 30*time.Minute, c.cfg.ATRRedisTTL)
	assert.Equal(t, 30*time.Minute, c.cfg.EMARedisTTL)
	assert.InDelta(t, 0.30, c.cfg.HighFailureRate, 1e-9)
	assert.Equal(t, "klines", c.Name())
}

func TestFetchSymbols_UsesCache(t *testing.T) {
	server, exiCalls, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, _, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{SymbolCacheTTL: time.Hour})
	_, err := c.fetchSymbols(context.Background())
	require.NoError(t, err)
	_, err = c.fetchSymbols(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, exiCalls.Load(), "second fetchSymbols within TTL must hit cache")
}

func TestKlinesRun_ConcurrentFetch_RespectsLimit(t *testing.T) {
	var inflight, peak atomic.Int32
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/exchangeInfo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(exchangeInfoFixture))
	})
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inflight.Add(-1)
		_, _ = w.Write([]byte(buildKlinesResp(constantCloses(30, "100"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	c, _, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{Concurrency: 2})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	require.NoError(t, <-done)
	assert.LessOrEqual(t, peak.Load(), int32(2), "peak in-flight requests must respect Concurrency=2")
}

func TestRun_BatchUpsertKlines_WritesAllRows(t *testing.T) {
	server, _, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, cap, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, cap.calls, 1)
	// 3 symbols × 30 bars = 90 rows.
	assert.Len(t, cap.calls[0], 90)
}

func TestRun_BatchUpsertKlines_HandlesPartialBatchFailure(t *testing.T) {
	server, _, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, _, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	c.writeFn = func(_ context.Context, p []gen.BatchUpsertKlinesParams) (int, error) {
		return len(p) / 2, errors.New("half-failed batch")
	}
	require.NotPanics(t, func() { _ = c.Run(context.Background()) })
}

func TestRun_ComputeATR_StoresInRedis(t *testing.T) {
	server, _, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, _, mr, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		p := readPayload(t, mr, "atr:"+sym)
		// Constant TR=1 fixture → ATR(14) = 1 exactly.
		assert.True(t, decimal.RequireFromString(p.Value).Equal(decimal.RequireFromString("1")), "atr:%s = %s, want 1", sym, p.Value)
		assert.NotEmpty(t, p.ComputedAt)
	}
	assert.Equal(t, 30*time.Minute, mr.TTL("atr:BTCUSDT"))
}

func TestRun_ComputeEMA_StoresInRedis(t *testing.T) {
	server, _, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, _, mr, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		p := readPayload(t, mr, "ema20:"+sym)
		// Constant close=100 → EMA(20) = 100 exactly (SMA seed + identity smoothing).
		assert.True(t, decimal.RequireFromString(p.Value).Equal(decimal.RequireFromString("100")), "ema20:%s = %s, want 100", sym, p.Value)
	}
	assert.Equal(t, 30*time.Minute, mr.TTL("ema20:BTCUSDT"))
}

func TestRun_ATRComputeError_ContinuesOtherSymbols(t *testing.T) {
	// BTCUSDT: 14 bars (ATR(14) needs >=15) → ATR fails. ETHUSDT/SOLUSDT: 30 bars → both ok.
	fix := map[string]string{"BTCUSDT": buildKlinesResp(constantCloses(14, "100"))}
	server, _, _ := newT7Server(t, fix, nil)
	defer server.Close()
	c, _, mr, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	_, errBTC := mr.Get("atr:BTCUSDT")
	require.Error(t, errBTC, "atr:BTCUSDT must NOT exist (insufficient bars)")
	for _, sym := range []string{"ETHUSDT", "SOLUSDT"} {
		_, err := mr.Get("atr:" + sym)
		require.NoError(t, err, "atr:%s missing", sym)
	}
}

func TestRun_EMAComputeError_ContinuesOtherSymbols(t *testing.T) {
	// BTCUSDT: 19 bars (EMA(20) needs >=20) → EMA fails. ATR(14) still ok (>=15).
	fix := map[string]string{"BTCUSDT": buildKlinesResp(constantCloses(19, "100"))}
	server, _, _ := newT7Server(t, fix, nil)
	defer server.Close()
	c, _, mr, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	_, errBTC := mr.Get("ema20:BTCUSDT")
	require.Error(t, errBTC, "ema20:BTCUSDT must NOT exist (insufficient bars)")
	_, atrErr := mr.Get("atr:BTCUSDT")
	require.NoError(t, atrErr, "atr:BTCUSDT must still exist (19 >= 15 for ATR(14))")
	for _, sym := range []string{"ETHUSDT", "SOLUSDT"} {
		_, err := mr.Get("ema20:" + sym)
		require.NoError(t, err, "ema20:%s missing", sym)
	}
}

func TestRun_RedisWriteError_LogsAndContinues(t *testing.T) {
	server, _, _ := newT7Server(t, nil, nil)
	defer server.Close()
	c, cap, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	// Point redis client to a closed port → all SETs fail.
	c.redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	require.NoError(t, c.Run(context.Background()), "DB write succeeded → Run should not error")
	require.Len(t, cap.calls, 1, "DB writeFn must still be called when Redis is dead")
	assert.Len(t, cap.calls[0], 90)
}

func TestRun_PartialFetchFailure_PartialDBWrite(t *testing.T) {
	// BTCUSDT 500s; ETH/SOL succeed → 2 symbols' rows reach writeFn.
	server, _, _ := newT7Server(t, nil, map[string]int{"BTCUSDT": 500})
	defer server.Close()
	c, cap, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{HighFailureRate: 0.99})
	require.NoError(t, c.Run(context.Background()))
	require.Len(t, cap.calls, 1)
	assert.Len(t, cap.calls[0], 60, "must persist rows from 2 successful symbols × 30 bars")
}

func TestKlinesRun_AllSymbolsFailed_ReturnsError(t *testing.T) {
	server, _, _ := newT7Server(t, nil, map[string]int{"BTCUSDT": 500, "ETHUSDT": 500, "SOLUSDT": 500})
	defer server.Close()
	c, _, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{HighFailureRate: 0.99})
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full-tick failure")
}

func TestRun_KlinesUpsertOnConflict_UpdatesValues(t *testing.T) {
	// Tick 1: last bar close=100. Tick 2: same open_time but close=105 (in-progress
	// bar moved). The collector must send the new value to writeFn so PG's
	// ON CONFLICT DO UPDATE refreshes the row.
	closes1 := constantCloses(30, "100")
	closes2 := constantCloses(30, "100")
	closes2[29] = "105"
	current := buildKlinesResp(closes1)
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/exchangeInfo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(exchangeInfoFixture))
	})
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := current
		mu.Unlock()
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	c, cap, _, _ := testKlinesCollector(t, server, KlinesCollectorConfig{})
	require.NoError(t, c.Run(context.Background()))
	mu.Lock()
	current = buildKlinesResp(closes2)
	mu.Unlock()
	c.symbolsAt = time.Time{} // expire cache so symbols re-fetch ok
	require.NoError(t, c.Run(context.Background()))

	require.Len(t, cap.calls, 2)
	last1 := cap.calls[0][len(cap.calls[0])-1]
	last2 := cap.calls[1][len(cap.calls[1])-1]
	assert.True(t, last1.OpenTime.Equal(last2.OpenTime), "same in-progress bar across ticks")
	assert.True(t, last1.Close.Equal(decimal.RequireFromString("100")), "tick1 close=100, got %s", last1.Close)
	assert.True(t, last2.Close.Equal(decimal.RequireFromString("105")), "tick2 close=105, got %s", last2.Close)
}
