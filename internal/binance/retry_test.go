package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/pkg/ratelimit"

	"github.com/rs/zerolog"
)

// retryTestClient creates a Client targeting a test HTTP server.
func retryTestClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	// Override backoffs for fast tests.
	retryBackoffs = []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}

	return &Client{
		mode:          "test", // bypass testnet write-base safety check
		apiKey:        "test-key",
		apiSecret:     "test-secret",
		restBaseRead:  srvURL,
		restBaseWrite: srvURL,
		proxy:         &directProxy{},
		directHTTP:    &http.Client{Timeout: 5 * time.Second},
		limiter:       ratelimit.NewTokenBucket(100, 100),
		nowFunc:       func() time.Time { return time.Unix(1700000000, 0) },
		log:           zerolog.Nop(),
	}
}

// directProxy is a ProxyManager that returns a real http.Client (no fake transport).
// Used by retry/idempotent tests where we DO want to hit a real httptest server.
type directProxy struct{}

func (directProxy) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return &http.Client{Timeout: 5 * time.Second}, "direct://test", nil
}
func (directProxy) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	return nil, "", nil
}
func (directProxy) ReportSuccess(_ string)          {}
func (directProxy) ReportFailure(_ string, _ error) {}
func (directProxy) Stats() ProxyStats               { return ProxyStats{Mode: "direct-test"} }


func TestDoWriteRetry_5xxRetries3Times(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":-1000,"msg":"server error"}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err)
	assert.Equal(t, int32(4), hits.Load(), "1 initial + 3 retries = 4 attempts")
}

func TestDoWriteRetry_5xxThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	body, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(body))
	assert.Equal(t, int32(3), hits.Load(), "2 fail + 1 success")
}

// v0.2 Gap 2: api_errors hook must fire AFTER retry budget exhausted (3 fails).
// Idempotent / treat-as-success classifications skip the hook so the rate
// trip doesn't count -4046 / -4059 / -2011 etc.
func TestDoWriteRetry_Exhausted_FiresAPIErrorHook(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":-1000,"msg":"server overloaded"}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	type hookCall struct {
		source, endpoint, message string
		httpCode, bizCode         int
	}
	var calls []hookCall
	c.SetAPIErrorHook(func(_ context.Context, source, endpoint string, httpCode, bizCode int, message string) {
		calls = append(calls, hookCall{source, endpoint, message, httpCode, bizCode})
	})

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err)
	assert.Equal(t, int32(4), hits.Load(), "1 initial + 3 retries = 4 attempts")
	require.Len(t, calls, 1, "hook fires EXACTLY once (after retries exhausted, not per attempt)")
	assert.Equal(t, "doWriteRetry", calls[0].source)
	assert.Equal(t, "/test", calls[0].endpoint)
	assert.Equal(t, 503, calls[0].httpCode)
	assert.Equal(t, -1000, calls[0].bizCode)
}

// v0.2 Gap 2: idempotent treat-as-success (-4046 / -4059) MUST NOT fire the
// api_errors hook. Otherwise the rate trip would count routine margin-type or
// leverage no-ops.
func TestDoWriteRetry_TreatAsSuccess_DoesNotFireHook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-4046,"msg":"No need to change margin type."}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	var hookCalls int32
	c.SetAPIErrorHook(func(_ context.Context, _, _ string, _, _ int, _ string) {
		hookCalls++
	})

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err, "doWriteRetry returns the -4046; caller (SetMarginType) decides to swallow")
	assert.Equal(t, int32(0), hookCalls, "-4046 must not fire hook (recordAPIError filter)")
}

func TestDoWriteRetry_PermanentNoRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-2019,"msg":"Margin is insufficient."}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err)
	assert.Equal(t, int32(1), hits.Load(), "permanent -2019 must NOT retry")
}

func TestDoWriteRetry_Minus1021OneRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-1021,"msg":"Timestamp outside recvWindow"}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err)
	assert.Equal(t, int32(2), hits.Load(), "-1021 retries exactly 1 time (1 initial + 1 retry)")
}

func TestDoWriteRetry_Minus2022PassesThroughForCaller(t *testing.T) {
	// -4116 must NOT retry; caller handles via GetOrderByClientID lookup.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-4116,"msg":"ClientOrderId is duplicated."}`))
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	_, err := c.doWriteRetry(context.Background(), http.MethodPost, "/test", nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-4116")
	assert.Equal(t, int32(1), hits.Load(), "-4116 must not retry — caller looks up by clientOrderId")
}

func TestPlaceMarketOrder_Minus2022LookupReturnsExisting(t *testing.T) {
	// 1st call: -4116. Caller (PlaceMarketOrder) then calls GetOrderByClientID
	// (GET /fapi/v1/order with origClientOrderId), returns FILLED.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/order") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-4116,"msg":"ClientOrderId is duplicated."}`))
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/order") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"orderId":42,"clientOrderId":"t100_r0","symbol":"BTCUSDT","status":"FILLED","avgPrice":"80000.0","executedQty":"0.006","cumQuote":"480.0","updateTime":1700000000000}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := retryTestClient(t, srv.URL)

	res, err := c.PlaceMarketOrder(context.Background(), "BTCUSDT", "BUY", "0.006", "t100_r0")
	t.Logf("res=%+v err=%v", res, err)
	require.NoError(t, err, "-4116 path with successful lookup must return existing order")
	assert.Equal(t, int64(42), res.OrderID)
	assert.Equal(t, "FILLED", res.Status)
}
