package binance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/config"
)

// ---- test fixtures -------------------------------------------------------

func testnetCfg() *config.Config {
	return &config.Config{
		Mode: "testnet",
		Binance: config.BinanceConfig{
			APIKey:    "test-key",
			APISecret: "test-secret",
		},
	}
}

func mainnetCfg() *config.Config {
	c := testnetCfg()
	c.Mode = "mainnet"
	c.MainnetConfirm = "I_UNDERSTAND"
	return c
}

// recordingTransport captures every outgoing request so tests can assert URL
// host, method, body, and headers without spinning up a real HTTP server.
type recordingTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	status   int
	respBody string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
	}
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.bodies = append(r.bodies, bodyStr)
	r.mu.Unlock()
	status := r.status
	if status == 0 {
		status = 200
	}
	body := r.respBody
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

// fakeProxy implements ProxyManager backed by a recordingTransport.
type fakeProxy struct {
	rec       *recordingTransport
	failures  int
	successes int
}

func newFakeProxy() *fakeProxy { return &fakeProxy{rec: &recordingTransport{}} }

func (f *fakeProxy) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return &http.Client{Transport: f.rec}, "fake://proxy", nil
}
func (f *fakeProxy) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	return nil, "", errors.New("WSDialer unused in client tests")
}
func (f *fakeProxy) ReportFailure(string, error) { f.failures++ }
func (f *fakeProxy) ReportSuccess(string)        { f.successes++ }
func (f *fakeProxy) Stats() ProxyStats           { return ProxyStats{Mode: "fake"} }

// swapClientSleep replaces the package-level mainnet pause with a noop and
// restores on cleanup so other tests are not affected.
func swapClientSleep(t *testing.T) {
	t.Helper()
	saved := clientSleep
	clientSleep = func(time.Duration) {}
	t.Cleanup(func() { clientSleep = saved })
}

func mustNewTestnet(t *testing.T, fp *fakeProxy) *Client {
	t.Helper()
	c, err := New(testnetCfg(), fp, nil, zerolog.Nop())
	require.NoError(t, err)
	return c
}

// ---- New() tests ---------------------------------------------------------

func TestNew_TestnetMode_RoutesCorrectly(t *testing.T) {
	c := mustNewTestnet(t, newFakeProxy())
	assert.Equal(t, MainnetREST, c.restBaseRead, "testnet mode reads from production")
	assert.Equal(t, TestnetREST, c.restBaseWrite, "testnet mode writes to testnet")
}

func TestNew_MainnetMode_RequiresConfirm(t *testing.T) {
	swapClientSleep(t)
	cfg := mainnetCfg()
	cfg.MainnetConfirm = "" // missing
	_, err := New(cfg, newFakeProxy(), nil, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
}

func TestNew_MainnetMode_PrintsWarning(t *testing.T) {
	swapClientSleep(t)
	var buf bytes.Buffer
	log := zerolog.New(&buf)
	c, err := New(mainnetCfg(), newFakeProxy(), nil, log)
	require.NoError(t, err)
	assert.Equal(t, MainnetREST, c.restBaseRead)
	assert.Equal(t, MainnetREST, c.restBaseWrite, "mainnet mode writes to production")
	assert.GreaterOrEqual(t, strings.Count(buf.String(), "MAINNET"), 5,
		"expected ≥5 MAINNET warnings, got:\n%s", buf.String())
}

func TestNew_RejectsUnknownMode(t *testing.T) {
	cfg := testnetCfg()
	cfg.Mode = "staging"
	_, err := New(cfg, newFakeProxy(), nil, zerolog.Nop())
	require.Error(t, err)
}

// ---- routing tests -------------------------------------------------------

func TestDoRead_GoesToMainnet_InTestnetMode(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	_, err := c.DoRead(context.Background(), "/fapi/v1/exchangeInfo", nil, 1)
	require.NoError(t, err)
	require.Len(t, fp.rec.requests, 1)
	assert.Equal(t, "fapi.binance.com", fp.rec.requests[0].URL.Host)
	assert.Equal(t, http.MethodGet, fp.rec.requests[0].Method)
}

func TestDoWrite_NormalEndpoint_GoesToTestnet(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	_, err := c.doWrite(context.Background(), http.MethodPost, "/fapi/v1/order", nil, 1)
	require.NoError(t, err)
	require.Len(t, fp.rec.requests, 1)
	assert.Equal(t, "testnet.binancefuture.com", fp.rec.requests[0].URL.Host)
	assert.Equal(t, "application/x-www-form-urlencoded", fp.rec.requests[0].Header.Get("Content-Type"))
	assert.Equal(t, "test-key", fp.rec.requests[0].Header.Get("X-MBX-APIKEY"))
}

func TestDoWrite_ListenKey_BypassesHardBlock(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	_, err := c.doWrite(context.Background(), http.MethodPost, "/fapi/v1/listenKey", nil, 1)
	require.NoError(t, err)
	require.Len(t, fp.rec.requests, 1)
	// Whitelist routes listenKey to the mode-matched host (testnet here).
	assert.Equal(t, "testnet.binancefuture.com", fp.rec.requests[0].URL.Host)
}

func TestDoWrite_HardBlock_WhenMisrouted(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	c.restBaseWrite = MainnetREST // simulate config drift / bug
	_, err := c.doWrite(context.Background(), http.MethodPost, "/fapi/v1/order", nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safety")
	assert.Empty(t, fp.rec.requests, "no request should be sent when hard-block fires")
}

// TestDoWrite_HardBlock_Exhaustive runs every plausible write path through
// doWrite in testnet mode and pins the routing rule: every write hits testnet,
// listenKey is whitelisted (also testnet here), nothing punches production.
func TestDoWrite_HardBlock_Exhaustive(t *testing.T) {
	writePaths := []string{
		"/fapi/v1/order",
		"/fapi/v1/leverage",
		"/fapi/v1/marginType",
		"/fapi/v1/positionMargin",
		"/fapi/v1/multiAssetsMargin",
		"/fapi/v1/algo/order",
		"/fapi/v1/algo/order/cancel",
		"/fapi/v1/listenKey", // whitelisted but still testnet in testnet mode
	}
	for _, path := range writePaths {
		t.Run(path, func(t *testing.T) {
			fp := newFakeProxy()
			c := mustNewTestnet(t, fp)
			_, err := c.doWrite(context.Background(), http.MethodPost, path, nil, 1)
			require.NoError(t, err)
			require.Len(t, fp.rec.requests, 1)
			assert.Equal(t, "testnet.binancefuture.com", fp.rec.requests[0].URL.Host,
				"path %q must route to testnet under testnet mode", path)
		})
	}
}

func TestDoWrite_RejectsNonWriteMethod(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	_, err := c.doWrite(context.Background(), http.MethodGet, "/fapi/v1/order", nil, 1)
	require.Error(t, err)
}

// ---- request shape tests -------------------------------------------------

func TestDoRequest_GET_SignsInQueryString(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	c.nowFunc = func() time.Time { return time.Unix(0, 1499827319559*int64(time.Millisecond)) }
	v := url.Values{}
	v.Set("symbol", "BTCUSDT")
	_, err := c.DoRead(context.Background(), "/fapi/v1/ping", v, 1)
	require.NoError(t, err)
	require.Len(t, fp.rec.requests, 1)
	q := fp.rec.requests[0].URL.RawQuery
	assert.Contains(t, q, "symbol=BTCUSDT")
	assert.Contains(t, q, "timestamp=1499827319559")
	assert.Contains(t, q, "signature=")
}

func TestDoRequest_POST_SignsInBody(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	v := url.Values{}
	v.Set("symbol", "BTCUSDT")
	_, err := c.doWrite(context.Background(), http.MethodPost, "/fapi/v1/order", v, 1)
	require.NoError(t, err)
	require.Len(t, fp.rec.bodies, 1)
	assert.Contains(t, fp.rec.bodies[0], "symbol=BTCUSDT")
	assert.Contains(t, fp.rec.bodies[0], "signature=")
	assert.Empty(t, fp.rec.requests[0].URL.RawQuery, "POST signed body, query must be empty")
}

func TestDoRequest_5XX_TriggersReportFailure(t *testing.T) {
	fp := newFakeProxy()
	fp.rec.status = 503
	c := mustNewTestnet(t, fp)
	_, err := c.DoRead(context.Background(), "/fapi/v1/ping", nil, 1)
	require.Error(t, err)
	assert.Equal(t, 1, fp.failures, "5XX must call ReportFailure")
	assert.Equal(t, 0, fp.successes)
}

func TestDoRequest_200_TriggersReportSuccess(t *testing.T) {
	fp := newFakeProxy()
	c := mustNewTestnet(t, fp)
	_, err := c.DoRead(context.Background(), "/fapi/v1/ping", nil, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, fp.failures)
	assert.Equal(t, 1, fp.successes)
}

// ---- helper tests --------------------------------------------------------

func TestIsListenKeyPath(t *testing.T) {
	cases := map[string]bool{
		"/fapi/v1/listenKey":       true,
		"/fapi/v1/listenKeyAdmin":  false, // prefix-only must NOT bypass
		"/fapi/v1/listenkey":       false, // case-sensitive
		"/fapi/v1/order":           false,
		"":                         false,
		"/fapi/v1/listenKey/extra": false,
	}
	for path, want := range cases {
		assert.Equal(t, want, isListenKeyPath(path), path)
	}
}

func TestIsWriteMethod(t *testing.T) {
	cases := map[string]bool{
		http.MethodPost:    true,
		http.MethodPut:     true,
		http.MethodDelete:  true,
		http.MethodGet:     false,
		http.MethodHead:    false,
		http.MethodOptions: false,
		http.MethodPatch:   false, // we don't use PATCH; reject by policy
		"":                 false,
	}
	for m, want := range cases {
		assert.Equal(t, want, isWriteMethod(m), m)
	}
}
