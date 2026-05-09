package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

// hashtagBody returns a /queryByHashtag response with the given counts.
func hashtagBody(contentCount, viewCount int64) string {
	return fmt.Sprintf(`{"code":"000000","data":{"hashtag":{"contentCount":%d,"viewCount":%d}}}`, contentCount, viewCount)
}

// newHashtagCollector wires a SquareHashtagCollector. srv=nil → no client
// (readWatchlist-only tests). db=nil → no queries (non-DB tests).
// RetryInterval is 1ms (vs 1s prod) so retry tests stay fast.
// Reuses 1.4 helpers from square_feed_test.go: squareTestProxy /
// noopLimiter / squareTestServer / fakeDBTX (same package).
func newHashtagCollector(t *testing.T, srv *httptest.Server, db gen.DBTX) *SquareHashtagCollector {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	var sc *square.SquareClient
	if srv != nil {
		target, _ := url.Parse(srv.URL)
		var err error
		sc, err = square.NewSquareClient(context.Background(), &squareTestProxy{target: target}, noopLimiter{}, rdb, true, zerolog.Nop())
		require.NoError(t, err)
	}
	var queries *gen.Queries
	if db != nil {
		queries = gen.New(db)
	}
	return &SquareHashtagCollector{
		client:  sc,
		redis:   rdb,
		queries: queries,
		log:     zerolog.Nop(),
		cfg: squareHashtagDefaults(SquareHashtagConfig{
			RetryInterval:    1 * time.Millisecond,
			PerSymbolTimeout: 500 * time.Millisecond,
			PerTickTimeout:   30 * time.Second,
		}),
		nowFunc: time.Now,
	}
}

func setWatchlist(t *testing.T, c *SquareHashtagCollector, symbols []string) {
	t.Helper()
	b, _ := json.Marshal(symbols)
	require.NoError(t, c.redis.Set(context.Background(), c.cfg.WatchlistRedisKey, b, 0).Err())
}

// --- readWatchlist (3) ---

func TestSquareHashtag_ReadWatchlist_NormalKey(t *testing.T) {
	c := newHashtagCollector(t, nil, nil)
	setWatchlist(t, c, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"})
	got, err := c.readWatchlist(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, got)
}

func TestSquareHashtag_ReadWatchlist_EmptyKey_ReturnsEmpty(t *testing.T) {
	c := newHashtagCollector(t, nil, nil)
	got, err := c.readWatchlist(context.Background())
	require.NoError(t, err, "redis.Nil must NOT bubble as error")
	assert.Empty(t, got)
}

func TestSquareHashtag_ReadWatchlist_RedisError_BubblesUp(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	c := &SquareHashtagCollector{redis: rdb, log: zerolog.Nop(), cfg: squareHashtagDefaults(SquareHashtagConfig{})}
	_, err := c.readWatchlist(context.Background())
	require.Error(t, err)
}

// --- Run, empty watchlist (1) ---

func TestSquareHashtagRun_EmptyWatchlist_SkipsWithWarn(t *testing.T) {
	c := newHashtagCollector(t, nil, nil)
	require.NoError(t, c.Run(context.Background()), "empty watchlist must skip not error")
}

// --- fetchSingleHashtag retry (5) ---

func TestFetchSingleHashtag_Success_NoRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		_, _ = w.Write([]byte(hashtagBody(100, 1000)))
	})
	c := newHashtagCollector(t, srv, nil)
	cc, vc, err := c.fetchSingleHashtag(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.EqualValues(t, 100, cc)
	assert.EqualValues(t, 1000, vc)
	assert.EqualValues(t, 1, attempts.Load(), "must not retry on success")
}

func TestFetchSingleHashtag_TransientError_Retries(t *testing.T) {
	var attempts atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(hashtagBody(100, 1000)))
	})
	c := newHashtagCollector(t, srv, nil)
	cc, _, err := c.fetchSingleHashtag(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.EqualValues(t, 100, cc)
	assert.EqualValues(t, 3, attempts.Load(), "should retry until success at attempt 3")
}

func TestFetchSingleHashtag_AllAttemptsFail_ReturnsError(t *testing.T) {
	var attempts atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(500)
	})
	c := newHashtagCollector(t, srv, nil)
	_, _, err := c.fetchSingleHashtag(context.Background(), "BTCUSDT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 2 retries")
	assert.EqualValues(t, 3, attempts.Load(), "1 + 2 retries = 3 attempts")
}

func TestFetchSingleHashtag_4xx_NoRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(400)
	})
	c := newHashtagCollector(t, srv, nil)
	_, _, err := c.fetchSingleHashtag(context.Background(), "BTCUSDT")
	require.Error(t, err)
	var sqErr *square.SquareError
	require.True(t, errors.As(err, &sqErr))
	assert.Equal(t, 400, sqErr.HTTPCode)
	assert.EqualValues(t, 1, attempts.Load(), "4xx must not retry")
}

func TestFetchSingleHashtag_CtxCancelled_ExitsImmediately(t *testing.T) {
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	c := newHashtagCollector(t, srv, nil)
	c.cfg.RetryInterval = 10 * time.Second // long enough that ctx cancel is the early exit
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, err := c.fetchSingleHashtag(ctx, "BTCUSDT")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 2*time.Second, "ctx cancel must cut 10s retry interval short — proves select{ctx.Done} works")
}

// --- parseHashtagResponse (3) ---

func TestParseHashtag_ValidResponse(t *testing.T) {
	cc, vc, err := parseHashtagResponse([]byte(hashtagBody(52244921, 8455370402)))
	require.NoError(t, err)
	assert.EqualValues(t, 52244921, cc)
	assert.EqualValues(t, 8455370402, vc)
}

func TestParseHashtag_MissingContentCount_ReturnsError(t *testing.T) {
	body := []byte(`{"code":"000000","data":{"hashtag":{"viewCount":1000}}}`)
	_, _, err := parseHashtagResponse(body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contentCount")
}

func TestParseHashtag_ZeroValues_NoError(t *testing.T) {
	cc, vc, err := parseHashtagResponse([]byte(hashtagBody(0, 0)))
	require.NoError(t, err, "0 is a legal value (new hashtag, no posts) — must not be confused with missing field")
	assert.EqualValues(t, 0, cc)
	assert.EqualValues(t, 0, vc)
}

// --- hashtag param case (1) ---

func TestFetchSingleHashtag_HashtagParam_IsLowercaseNoHash(t *testing.T) {
	var capturedHashtag string
	srv := squareTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedHashtag = r.URL.Query().Get("hashtag")
		_, _ = w.Write([]byte(hashtagBody(100, 1000)))
	})
	c := newHashtagCollector(t, srv, nil)
	_, _, err := c.fetchSingleHashtag(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, "btc", capturedHashtag, "hashtag query param must be lowercase, no '#' prefix (per square-discussion.py)")
}

// --- batch insert (2) ---

func TestSquareHashtag_BatchInsert_FiltersErroredResults(t *testing.T) {
	c := newHashtagCollector(t, nil, &fakeDBTX{})
	results := []hashtagResult{
		{symbol: "BTCUSDT", contentCount: 100, viewCount: 1000, err: nil},
		{symbol: "ETHUSDT", err: errors.New("fetch failed")},
		{symbol: "SOLUSDT", contentCount: 50, viewCount: 500, err: nil},
	}
	success := c.batchInsertHashtagHistory(context.Background(), results)
	assert.Equal(t, 2, success, "errored result must be filtered, only 2 rows submitted")
}

func TestSquareHashtagRun_PartialFailure_ContinuesOthers(t *testing.T) {
	srv := squareTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hashtag") == "eth" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(hashtagBody(100, 1000)))
	})
	c := newHashtagCollector(t, srv, &fakeDBTX{})
	setWatchlist(t, c, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"})
	require.NoError(t, c.Run(context.Background()), "partial failure must not error the run")
}
