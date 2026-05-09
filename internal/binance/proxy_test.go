package binance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/config"
)

// cfgWithProxy builds a minimal *config.Config for the proxy layer's needs.
// All other Config fields are zero — fine, the proxy layer only reads cfg.Proxy.
func cfgWithProxy(mode string, urls []string) *config.Config {
	return &config.Config{
		Proxy: config.ProxyConfig{
			Mode:             mode,
			URL:              "http://single.example.com:8080",
			PoolURLs:         urls,
			PoolStrategy:     "round_robin",
			FailureThreshold: 5,
			RecoveryMinutes:  5,
		},
	}
}

// poolFromURLs constructs a Pool with controllable strategy and asserts the
// concrete *Pool type so tests can exercise nowFunc and internal state.
func poolFromURLs(t *testing.T, urls []string, strategy string) *Pool {
	t.Helper()
	cfg := cfgWithProxy("pool", urls)
	cfg.Proxy.PoolStrategy = strategy
	pm, err := New(cfg)
	require.NoError(t, err)
	p, ok := pm.(*Pool)
	require.True(t, ok)
	return p
}

func TestNew_NoneMode(t *testing.T) {
	pm, err := New(cfgWithProxy("none", nil))
	require.NoError(t, err)
	_, ok := pm.(*noopManager)
	assert.True(t, ok)
	c, raw, err := pm.HTTPClient(context.Background())
	require.NoError(t, err)
	assert.Empty(t, raw)
	assert.NotNil(t, c)
	assert.Equal(t, ProxyStats{Mode: "none"}, pm.Stats())
}

func TestNew_SingleMode(t *testing.T) {
	pm, err := New(cfgWithProxy("single", nil))
	require.NoError(t, err)
	_, raw, err := pm.HTTPClient(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "http://single.example.com:8080", raw)
	// ReportFailure on single is a noop.
	pm.ReportFailure(raw, errors.New("ignored"))
	assert.Equal(t, 1, pm.Stats().ActiveCount)
}

func TestNew_PoolMode_RoundRobin(t *testing.T) {
	urls := []string{
		"http://p1.example:8080",
		"http://p2.example:8080",
		"http://p3.example:8080",
	}
	p := poolFromURLs(t, urls, "round_robin")
	seen := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		_, raw, err := p.HTTPClient(context.Background())
		require.NoError(t, err)
		seen = append(seen, raw)
	}
	expected := []string{urls[0], urls[1], urls[2], urls[0], urls[1], urls[2]}
	assert.Equal(t, expected, seen)
}

func TestNew_PoolMode_Random(t *testing.T) {
	urls := []string{
		"http://p1.example:8080",
		"http://p2.example:8080",
		"http://p3.example:8080",
	}
	p := poolFromURLs(t, urls, "random")
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		_, raw, err := p.HTTPClient(context.Background())
		require.NoError(t, err)
		counts[raw]++
	}
	assert.Len(t, counts, 3, "every proxy should be picked at least once across 300 trials")
	for _, n := range counts {
		assert.Greater(t, n, 0)
	}
}

func TestNew_PoolMode_EmptyURLs_Error(t *testing.T) {
	_, err := New(cfgWithProxy("pool", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one proxy URL")
}

func TestPool_ReportFailure_Eviction(t *testing.T) {
	p := poolFromURLs(t, []string{
		"http://p1.example:8080",
		"http://p2.example:8080",
	}, "round_robin")
	// Threshold 5: first 4 don't evict.
	for i := 0; i < 4; i++ {
		p.ReportFailure("http://p1.example:8080", errors.New("test"))
	}
	assert.Equal(t, 0, p.Stats().EvictedCount)
	p.ReportFailure("http://p1.example:8080", errors.New("test"))
	assert.Equal(t, 1, p.Stats().EvictedCount)
}

func TestPool_ReportFailure_AllEvicted(t *testing.T) {
	urls := []string{"http://p1.example:8080", "http://p2.example:8080"}
	p := poolFromURLs(t, urls, "round_robin")
	for _, u := range urls {
		for i := 0; i < 5; i++ {
			p.ReportFailure(u, errors.New("test"))
		}
	}
	_, _, err := p.HTTPClient(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all proxies evicted")
}

func TestPool_PassiveRecovery(t *testing.T) {
	p := poolFromURLs(t, []string{
		"http://p1.example:8080",
		"http://p2.example:8080",
	}, "round_robin")
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p.nowFunc = func() time.Time { return base }
	for i := 0; i < 5; i++ {
		p.ReportFailure("http://p1.example:8080", errors.New("x"))
	}
	assert.Equal(t, 1, p.Stats().EvictedCount)

	// Advance past RecoveryMinutes (5) — p1 becomes a probe candidate.
	p.nowFunc = func() time.Time { return base.Add(6 * time.Minute) }
	assert.Equal(t, 0, p.Stats().EvictedCount, "p1 should be eligible again after recovery window")

	// Force enough rotations that p1 must be selected (round-robin over 2 entries).
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		_, raw, err := p.HTTPClient(context.Background())
		require.NoError(t, err)
		seen[raw]++
	}
	assert.Greater(t, seen["http://p1.example:8080"], 0, "p1 must be selected after recovery")
}

func TestPool_ReportSuccess_ResetsCount(t *testing.T) {
	p := poolFromURLs(t, []string{"http://p1.example:8080"}, "round_robin")
	for i := 0; i < 4; i++ {
		p.ReportFailure("http://p1.example:8080", errors.New("x"))
	}
	p.ReportSuccess("http://p1.example:8080")
	// After success the counter is 0; another 4 failures must not evict.
	for i := 0; i < 4; i++ {
		p.ReportFailure("http://p1.example:8080", errors.New("x"))
	}
	assert.Equal(t, 0, p.Stats().EvictedCount)
}

func TestPool_HTTPClient_ProxyConfigured(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := poolFromURLs(t, []string{server.URL}, "round_robin")
	c, raw, err := p.HTTPClient(context.Background())
	require.NoError(t, err)
	assert.Equal(t, server.URL, raw)

	// httptest server acts as our HTTP proxy; client request is forwarded to it.
	resp, err := c.Get("http://example.com/test")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(1), hits.Load())
}

func TestPool_WSDialer_HTTP(t *testing.T) {
	p := poolFromURLs(t, []string{"http://proxy.example:8080"}, "round_robin")
	d, raw, err := p.WSDialer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "http://proxy.example:8080", raw)
	require.NotNil(t, d.Proxy)
	assert.Nil(t, d.NetDialContext, "http proxy should use Dialer.Proxy, not NetDialContext")
	req, _ := http.NewRequest(http.MethodGet, "ws://target.example", nil)
	proxyURL, err := d.Proxy(req)
	require.NoError(t, err)
	assert.Equal(t, "proxy.example:8080", proxyURL.Host)
}

func TestPool_WSDialer_Socks5(t *testing.T) {
	p := poolFromURLs(t, []string{"socks5://user:pw@localhost:1080"}, "round_robin")
	d, raw, err := p.WSDialer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "socks5://user:pw@localhost:1080", raw)
	require.NotNil(t, d.NetDialContext, "socks5 should set NetDialContext")
	assert.Nil(t, d.Proxy, "socks5 should not set Dialer.Proxy")
}

// TestPool_Concurrent_HTTPClient_Race must pass under -race. It hammers the
// pool from 100 goroutines doing mixed HTTPClient + ReportFailure/Success.
func TestPool_Concurrent_HTTPClient_Race(t *testing.T) {
	urls := []string{
		"http://p1.example:8080",
		"http://p2.example:8080",
		"http://p3.example:8080",
	}
	p := poolFromURLs(t, urls, "round_robin")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = p.HTTPClient(context.Background())
			target := fmt.Sprintf("http://p%d.example:8080", (i%3)+1)
			if i%2 == 0 {
				p.ReportFailure(target, errors.New("x"))
			} else {
				p.ReportSuccess(target)
			}
			_ = p.Stats()
		}(i)
	}
	wg.Wait()
}
