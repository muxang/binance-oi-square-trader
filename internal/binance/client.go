package binance

import (
	"context"
	"errors"
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

// APIErrorHook is invoked when an API error surfaces to business code AFTER
// retries are exhausted (for retry-capable paths) or on the first failure
// (for non-retry paths). Used by main.go to wire an api_errors table INSERT
// so v0.2 Round 6 TripAPIErrorRate can count actual errors.
//
// Idempotent / treat-as-success classifications (-4046 margin no-op, -4059
// leverage no-op, -2011/-2013 cancel-already-gone) are filtered out by
// recordAPIError so they don't trigger the rate trip.
//
// httpCode + bizCode = 0 means a non-API error (network / context cancel etc.);
// hook callers should still record those.
type APIErrorHook func(ctx context.Context, source, endpoint string, httpCode, bizCode int, message string)

// Production / testnet REST hosts. WS hosts are referenced in the WS layer.
//
// ref: references/binance/urls.md §「网络环境」
const (
	MainnetREST = "https://fapi.binance.com"
	TestnetREST = "https://testnet.binancefuture.com"
	MainnetWS   = "wss://fstream.binance.com"
	TestnetWS   = "wss://stream.binancefuture.com"
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
	directHTTP    *http.Client // direct (no-proxy) client for account/trading calls
	limiter       RateLimiter
	log           zerolog.Logger
	nowFunc       func() time.Time
	onAPIError    APIErrorHook // v0.2 Gap 2: api_errors auto-populate hook
}

// SetAPIErrorHook registers the post-error callback. Idempotent. Designed to
// be called once during wiring (main.go) after the DB is available; nil hook
// is a no-op.
func (c *Client) SetAPIErrorHook(hook APIErrorHook) {
	c.onAPIError = hook
}

// recordAPIError invokes onAPIError iff err is a "real" error worth counting.
// Skipped when err is nil OR ClassifyError says treat-as-success / treat-as-
// canceled (idempotent business no-ops should not contribute to the 1min rate
// trip). Non-API errors (network, context) are recorded with httpCode=bizCode=0.
func (c *Client) recordAPIError(ctx context.Context, source, endpoint string, err error) {
	if err == nil || c.onAPIError == nil {
		return
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		action := ClassifyError(apiErr.HTTPCode, apiErr.BizCode)
		if action == ActionTreatAsSuccess || action == ActionTreatAsCanceled {
			return
		}
		c.onAPIError(ctx, source, endpoint, apiErr.HTTPCode, apiErr.BizCode, apiErr.Message)
		return
	}
	// Network / context / proxy errors — count with zero codes.
	c.onAPIError(ctx, source, endpoint, 0, 0, err.Error())
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
		mode:       cfg.Mode,
		apiKey:     cfg.Binance.APIKey,
		apiSecret:  cfg.Binance.APISecret,
		proxy:      proxy,
		directHTTP: &http.Client{Timeout: 15 * time.Second},
		limiter:    limiter,
		log:        log,
		nowFunc:    timez.NowUTC,
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
// Used for MARKET-data endpoints (klines, ticker, openInterest, premiumIndex
// etc.) where we want real data on testnet for evaluation.
// All read paths are weight-counted via the limiter.
func (c *Client) DoRead(ctx context.Context, path string, params url.Values, weight int) ([]byte, error) {
	body, err := c.doRequest(ctx, http.MethodGet, c.restBaseRead, path, params, weight)
	c.recordAPIError(ctx, "DoRead", path, err)
	return body, err
}

// DoReadAccount issues a signed GET against the WRITE base (testnet in testnet
// mode). Used for ACCOUNT-data endpoints (positionRisk, account, balance,
// userTrades, listOpenOrders) where the API key is testnet-scoped and would
// fail with -2015 against the mainnet read base.
//
// This routing aligns with our defence-in-depth: testnet API keys can ONLY
// hit testnet base, full stop.
func (c *Client) DoReadAccount(ctx context.Context, path string, params url.Values, weight int) ([]byte, error) {
	body, err := c.doRequestDirect(ctx, http.MethodGet, c.restBaseWrite, path, params, weight)
	c.recordAPIError(ctx, "DoReadAccount", path, err)
	return body, err
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
	return c.doRequestDirect(ctx, method, base, path, params, weight)
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

// doRequestDirect is identical to doRequest but bypasses the ProxyManager and
// sends directly from the VPS IP. Used for account/trading endpoints that are
// IP-whitelisted on the Binance API key — proxy egress IPs cause -2015.
func (c *Client) doRequestDirect(ctx context.Context, method, base, path string, params url.Values, weight int) ([]byte, error) {
	signed := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			signed.Add(k, v)
		}
	}
	signed.Set("timestamp", strconv.FormatInt(c.nowFunc().UnixMilli(), 10))
	signed.Set("recvWindow", "60000")
	if err := c.limiter.Acquire(ctx, weight); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
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

	resp, err := c.directHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 || resp.StatusCode == 418 || resp.StatusCode == 429 {
		apiErr, _ := ParseError(resp)
		return nil, apiErr
	}
	if resp.StatusCode >= 400 {
		apiErr, _ := ParseError(resp)
		return nil, apiErr
	}
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
