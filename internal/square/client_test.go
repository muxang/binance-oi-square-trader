package square

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
)

type rewritingTransport struct{ target *url.URL }

func (r *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

type fakeProxy struct {
	target    *url.URL
	successes atomic.Int32
	failures  atomic.Int32
}

func (f *fakeProxy) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return &http.Client{Transport: &rewritingTransport{target: f.target}}, "fake://proxy", nil
}
func (f *fakeProxy) WSDialer(context.Context) (*websocket.Dialer, string, error) {
	return nil, "", errors.New("unused")
}
func (f *fakeProxy) ReportFailure(string, error) { f.failures.Add(1) }
func (f *fakeProxy) ReportSuccess(string)        { f.successes.Add(1) }
func (f *fakeProxy) Stats() binance.ProxyStats   { return binance.ProxyStats{Mode: "fake"} }

type countingLimiter struct{ acquireN atomic.Int32 }

func (l *countingLimiter) Acquire(_ context.Context, _ int) error {
	l.acquireN.Add(1)
	return nil
}

func newServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func newTestClient(t *testing.T, srv *httptest.Server, useProxy bool) (*SquareClient, *fakeProxy, *miniredis.Miniredis, *countingLimiter) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	target, _ := url.Parse(srv.URL)
	fp := &fakeProxy{target: target}
	cl := &countingLimiter{}
	c, err := NewSquareClient(context.Background(), fp, cl, rdb, useProxy, zerolog.Nop())
	require.NoError(t, err)
	if !useProxy {
		c.httpClient = &http.Client{Transport: &rewritingTransport{target: target}}
	}
	return c, fp, mr, cl
}

func TestNewSquareClient_GeneratesUUID_WhenRedisEmpty(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	c, _, mr, _ := newTestClient(t, srv, true)
	assert.NotEmpty(t, c.bncUUID)
	stored, err := mr.Get("bnc_uuid")
	require.NoError(t, err)
	assert.Equal(t, c.bncUUID, stored, "UUID must persist to Redis")
}

func TestNewSquareClient_ReusesUUID_WhenRedisHas(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	require.NoError(t, mr.Set("bnc_uuid", "preexisting-uuid"))
	c, err := NewSquareClient(context.Background(), &fakeProxy{}, &countingLimiter{}, rdb, true, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "preexisting-uuid", c.bncUUID)
}

func TestNewSquareClient_RedisError_FailsFast(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	_, err := NewSquareClient(context.Background(), &fakeProxy{}, &countingLimiter{}, rdb, true, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init bnc_uuid")
}

func TestDoPost_SendsAllHeaders(t *testing.T) {
	captured := http.Header{}
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.Header {
			captured[k] = v
		}
		_, _ = w.Write([]byte(`{}`))
	})
	c, _, _, _ := newTestClient(t, srv, true)
	_, err := c.DoPost(context.Background(), "/bapi/test", map[string]int{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, "application/json", captured.Get("Content-Type"))
	assert.Equal(t, "Mozilla/5.0", captured.Get("User-Agent"))
	assert.Equal(t, c.bncUUID, captured.Get("Bnc-Uuid"))
	assert.Equal(t, "web", captured.Get("Clienttype"))
	assert.Equal(t, "web", captured.Get("Versioncode"))
	assert.Equal(t, "https://www.binance.com", captured.Get("Origin"))
	assert.Equal(t, "https://www.binance.com/zh-CN/square", captured.Get("Referer"))
	assert.Contains(t, captured.Get("Cookie"), "bnc-uid="+c.bncUUID)
	assert.Contains(t, captured.Get("Cookie"), "lang=zh-CN")
}

func TestDoPost_BodyMarshaledCorrectly(t *testing.T) {
	var got []byte
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	})
	c, _, _, _ := newTestClient(t, srv, true)
	body := FeedRecommendRequest{PageIndex: 1, PageSize: 50, Scene: "web-homepage", ContentIds: []string{"a", "b"}}
	_, err := c.DoPost(context.Background(), "/bapi/test", body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"pageIndex":1,"pageSize":50,"scene":"web-homepage","contentIds":["a","b"]}`, string(got))
}

func TestDoPost_2xx_ReturnsBody(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":"hello"}`))
	})
	c, _, _, _ := newTestClient(t, srv, true)
	body, err := c.DoPost(context.Background(), "/bapi/test", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":"hello"}`, string(body))
}

func TestDoPost_NonOK_ReturnsSquareError(t *testing.T) {
	for _, code := range []int{500, 429, 401} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
			c, _, _, _ := newTestClient(t, srv, true)
			_, err := c.DoPost(context.Background(), "/bapi/test", nil)
			var sqErr *SquareError
			require.True(t, errors.As(err, &sqErr))
			assert.Equal(t, code, sqErr.HTTPCode)
		})
	}
}

func TestDoPost_RateLimiterAcquired(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	c, _, _, cl := newTestClient(t, srv, true)
	_, err := c.DoPost(context.Background(), "/bapi/test", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, cl.acquireN.Load(), "DoPost must Acquire once per call")
}

func TestDoPost_ProxyReportSuccess_On2xx(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	c, fp, _, _ := newTestClient(t, srv, true)
	_, err := c.DoPost(context.Background(), "/bapi/test", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fp.successes.Load())
	assert.EqualValues(t, 0, fp.failures.Load())
}

func TestDoPost_ProxyReportFailure_On5xx(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	c, fp, _, _ := newTestClient(t, srv, true)
	_, _ = c.DoPost(context.Background(), "/bapi/test", nil)
	assert.EqualValues(t, 1, fp.failures.Load())
	assert.EqualValues(t, 0, fp.successes.Load())
}

func TestDoPost_NoProxy_DirectClient(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":1}`)) })
	c, fp, _, _ := newTestClient(t, srv, false)
	body, err := c.DoPost(context.Background(), "/bapi/test", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":1}`, string(body))
	assert.EqualValues(t, 0, fp.successes.Load(), "proxy must not be touched when useProxy=false")
	assert.EqualValues(t, 0, fp.failures.Load())
}
