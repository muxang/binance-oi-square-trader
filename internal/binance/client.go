package binance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/config"
	"trader/internal/pkg/timez"
)

// Production / testnet REST hosts. WS hosts are referenced in the WS layer.
//
// ref: references/binance/urls.md §「网络环境」
const (
	MainnetREST = "https://fapi.binance.com"
	TestnetREST = "https://testnet.binancefuture.com"
)

// clientSleep is a test seam for the mainnet 5-second pause inside New.
// Tests swap to a noop with t.Cleanup-restored assignment.
var clientSleep = time.Sleep

// RateLimiter throttles outgoing requests by Binance request weight. Phase 0
// ships a noop; Phase 1 wires a token bucket without changing this signature.
// The Wait-style API (blocks until weight is available, ctx-cancellable) means
// business code stays oblivious to throttling — call it, get a response, done.
type RateLimiter interface {
	Acquire(ctx context.Context, weight int) error
}

type noopRateLimiter struct{}

func (noopRateLimiter) Acquire(_ context.Context, _ int) error { return nil }

// NewNoopRateLimiter returns the Phase 0 placeholder limiter — every Acquire
// returns nil immediately. Phase 1 swaps the *RateLimiter argument to New for a
// real token-bucket implementation; business code stays unchanged.
func NewNoopRateLimiter() RateLimiter { return noopRateLimiter{} }

// Client encapsulates Binance USDⓈ-M Futures REST access. Read base and write
// base are separate so testnet mode can pull real OI from production while
// hard-blocking writes onto testnet (SPEC §11.5).
type Client struct {
	mode          string
	apiKey        string
	apiSecret     string
	restBaseRead  string
	restBaseWrite string
	proxy         ProxyManager
	limiter       RateLimiter
	log           zerolog.Logger
	nowFunc       func() time.Time
}

// New constructs a Client. In mainnet mode it asserts TRADER_MAINNET_CONFIRM
// and prints 5 ⚠️ warnings + a 5-second pause to give the operator a chance
// to abort before any real-money request goes out.
//
// ref: ARCHITECTURE.md §11.5
func New(cfg *config.Config, proxy ProxyManager, limiter RateLimiter, log zerolog.Logger) (*Client, error) {
	if cfg.Mode != "testnet" && cfg.Mode != "mainnet" {
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	if proxy == nil {
		return nil, fmt.Errorf("proxy manager is required")
	}
	if limiter == nil {
		limiter = noopRateLimiter{}
	}
	c := &Client{
		mode:      cfg.Mode,
		apiKey:    cfg.Binance.APIKey,
		apiSecret: cfg.Binance.APISecret,
		proxy:     proxy,
		limiter:   limiter,
		log:       log,
		nowFunc:   timez.NowUTC,
	}
	if cfg.Mode == "mainnet" {
		if cfg.MainnetConfirm != "I_UNDERSTAND" {
			return nil, fmt.Errorf("mainnet mode requires TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
		}
		c.restBaseRead = MainnetREST
		c.restBaseWrite = MainnetREST
		for i := 0; i < 5; i++ {
			log.Warn().Msg("⚠️ MAINNET MODE — REAL MONEY. Pre-flight checklist verified?")
		}
		clientSleep(5 * time.Second)
	} else {
		c.restBaseRead = MainnetREST  // real OI / klines / etc.
		c.restBaseWrite = TestnetREST // safe for test orders
	}
	return c, nil
}

// DoRead issues a signed GET against the read base (production in either mode).
// All read paths are weight-counted via the limiter.
func (c *Client) DoRead(ctx context.Context, path string, params url.Values, weight int) ([]byte, error) {
	return c.doRequest(ctx, http.MethodGet, c.restBaseRead, path, params, weight)
}

// doWrite issues a signed POST/PUT/DELETE. listenKey paths route to the
// mode-matched base (testnet listenKey for testnet WS); other writes go to
// restBaseWrite, with a hard-block assertion that testnet mode never punches
// production writes — this is the defence-in-depth layer SPEC §11.5 demands.
func (c *Client) doWrite(ctx context.Context, method, path string, params url.Values, weight int) ([]byte, error) {
	if !isWriteMethod(method) {
		return nil, fmt.Errorf("doWrite called with non-write method %q", method)
	}
	base := c.restBaseWrite
	switch {
	case isListenKeyPath(path):
		// Whitelist: WS host must match the REST host that issued the listenKey.
		if c.mode == "testnet" {
			base = TestnetREST
		} else {
			base = MainnetREST
		}
	case c.mode == "testnet" && base != TestnetREST:
		return nil, fmt.Errorf("safety: testnet mode but write base %q is not testnet", base)
	}
	return c.doRequest(ctx, method, base, path, params, weight)
}

// doRequest is the single egress point: it acquires rate budget, signs, sends,
// reports the proxy outcome, and returns body bytes or an *APIError.
func (c *Client) doRequest(ctx context.Context, method, base, path string, params url.Values, weight int) ([]byte, error) {
	signed := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			signed.Add(k, v)
		}
	}
	signed.Set("timestamp", strconv.FormatInt(c.nowFunc().UnixMilli(), 10))
	// recvWindow=60000 (Binance max) tolerates occasional proxy latency spikes
	// that would otherwise cause -1021 "Timestamp outside recvWindow" rejections.
	signed.Set("recvWindow", "60000")
	if err := c.limiter.Acquire(ctx, weight); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	httpClient, proxyURL, err := c.proxy.HTTPClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("proxy http client: %w", err)
	}

	var (
		reqURL string
		body   io.Reader
	)
	if method == http.MethodGet {
		reqURL = base + path + "?" + BuildQueryString(signed, c.apiSecret)
	} else {
		reqURL = base + path
		body = strings.NewReader(BuildBody(signed, c.apiSecret))
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		c.proxy.ReportFailure(proxyURL, err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 || resp.StatusCode == 418 || resp.StatusCode == 429 {
		c.proxy.ReportFailure(proxyURL, fmt.Errorf("http %d", resp.StatusCode))
		apiErr, _ := ParseError(resp)
		return nil, apiErr
	}
	if resp.StatusCode >= 400 {
		apiErr, _ := ParseError(resp)
		return nil, apiErr
	}
	c.proxy.ReportSuccess(proxyURL)
	return io.ReadAll(resp.Body)
}

func isWriteMethod(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodDelete
}

// isListenKeyPath matches ONLY the exact listenKey path. Prefix matching would
// be unsafe — a hypothetical "/fapi/v1/listenKeyAdmin" must NOT bypass the
// hard-block.
func isListenKeyPath(path string) bool {
	return path == "/fapi/v1/listenKey"
}
