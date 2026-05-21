// Package coingecko provides a minimal Demo (free) API client for fetching
// circulating_supply data used in market_cap_ratio computation.
//
// ref: references/external/coingecko.md
// ref: references/user-snippets/contract-monitor.js (getCirculatingSupply / calculateMarketCapRatio)
//
// fetched: 2026-05-20
//
// Demo plan limits: ~30 calls/min (per official docs page). This project uses
// ≤8 calls/day (6h cron × ≤2 batches), so explicit rate limiting is omitted.
// Failures are non-fatal — callers log + continue with NULL market_cap_ratio.
package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Demo (free) base URL. Pro is intentionally unsupported.
const DemoBaseURL = "https://api.coingecko.com/api/v3"

// HTTPError carries non-2xx response bodies for triage. CoinGecko free tier
// returns 429 when over rate (rare at our cadence) and 5xx on outages.
type HTTPError struct {
	HTTPCode int
	Body     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("coingecko: HTTP %d body=%s", e.HTTPCode, e.Body)
}

// CoinInfo is one entry from GET /coins/list — symbol→id mapping. symbol is
// lowercased ("btc"), id is the CoinGecko-internal slug ("bitcoin"). Multiple
// coins can share a symbol (e.g. "luna" → both terra-luna-2 and terra-luna);
// callers disambiguate by market_cap (largest wins) after /coins/markets.
type CoinInfo struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

// MarketData is one entry from GET /coins/markets. Only fields used by
// market_cap_ratio are surfaced; the BAPI returns ~30+ more fields.
type MarketData struct {
	ID                string  `json:"id"`
	Symbol            string  `json:"symbol"`
	CurrentPrice      float64 `json:"current_price"`
	MarketCap         float64 `json:"market_cap"`
	CirculatingSupply float64 `json:"circulating_supply"`
}

// HTTPClientFn returns a per-request *http.Client. With a proxy pool the
// returned client may rotate IPs per call — CoinGecko Demo throttle is
// per-IP, so rotation effectively multiplies the rate ceiling.
//
// Signature mirrors binance.ProxyManager.HTTPClient minus the proxy label
// (we don't need it for logging here). nil result means use the package-
// internal direct http.Client.
type HTTPClientFn func(ctx context.Context) (*http.Client, error)

// Client is goroutine-safe; share one instance across all collectors.
type Client struct {
	baseURL    string
	apiKey     string // Demo key; empty = unauthenticated (slower rate, still works)
	directHTTP *http.Client
	// R.12 (2026-05-21 mu request): when set, every HTTP call goes through
	// this factory — e.g. binance.ProxyPool — so requests rotate source IPs
	// and bypass per-IP rate limits without relying on a paid API key.
	proxyHTTP HTTPClientFn
	// Demo plan throttles ~30 req/min burst; 2 collectors firing in parallel
	// hit 429 within seconds. Internal serialiser caps to ~24 req/min global.
	rateMu      sync.Mutex
	lastCallAt  time.Time
	minInterval time.Duration
}

// NewClient constructs a Demo-API client. apiKey may be "" for unauthenticated
// access (still works, lower rate limits). 15s timeout matches Square client
// style; CoinGecko Demo p99 is well under that even on slow days.
//
// Internal rate limiter paces calls to ≥2500ms apart globally — Demo plan
// 429 with burst, this serialiser avoids that even when symbol_map + supply
// collectors run concurrently at startup.
//
// Use WithProxyHTTP to swap the per-call http.Client factory for proxy-pool
// rotation.
func NewClient(apiKey string) *Client {
	return &Client{
		baseURL:     DemoBaseURL,
		apiKey:      apiKey,
		directHTTP:  &http.Client{Timeout: 15 * time.Second},
		minInterval: 2500 * time.Millisecond,
	}
}

// WithProxyHTTP wires a per-call http.Client factory (typically pulling from
// a binance ProxyPool) so CoinGecko requests rotate IPs. Returns the client
// itself for fluent setup.
func (c *Client) WithProxyHTTP(fn HTTPClientFn) *Client {
	c.proxyHTTP = fn
	return c
}

// withCustomBase exists for tests; production callers use NewClient.
func (c *Client) withCustomBase(base string) *Client {
	c.baseURL = base
	return c
}

// NewTestClient is an exported test seam: returns a Client wired to baseURL
// (typically an httptest server URL). Production code must use NewClient.
// Kept exported so cross-package collectors can wire a fake CoinGecko backend
// without re-implementing the HTTP layer.
func NewTestClient(baseURL string) *Client {
	return NewClient("").withCustomBase(baseURL)
}

// ListCoins fetches the full (id, symbol, name) catalog via /coins/list.
// ~15k entries as of 2026; call sparingly (daily startup refresh is enough).
// include_platform=false keeps the response small (no per-chain contract list).
func (c *Client) ListCoins(ctx context.Context) ([]CoinInfo, error) {
	q := url.Values{"include_platform": {"false"}}
	body, err := c.do(ctx, "/coins/list", q)
	if err != nil {
		return nil, err
	}
	var out []CoinInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse /coins/list: %w", err)
	}
	return out, nil
}

// GetMarketsTopByMcap fetches one page of /coins/markets ordered by
// market_cap_desc. limit ≤250 per docs. Use GetMarketsTopByMcapMulti for
// totals beyond 250 — wraps multi-page pulls.
func (c *Client) GetMarketsTopByMcap(ctx context.Context, limit int) ([]MarketData, error) {
	return c.getMarketsByMcapPage(ctx, limit, 1)
}

// GetMarketsTopByMcapMulti pulls totalLimit coins by market_cap_desc across
// multiple pages (250 per page). totalLimit clamped to [1, 2000].
//
// Added 2026-05-21 (R.12.B follow-up): top-250 alone leaves out alt coins
// in the $10M-$1B mcap range — EDEN/PUMP/etc — so the shortest-id fallback
// mis-mapped them. Top-1000 covers virtually every alt mu cares about,
// fallback only triggers for genuine micro-caps (<$10M).
//
// 4 batch calls / 6h = trivial vs CoinGecko Demo 30 req/min.
//
// Partial failures (rate limit on later pages) are tolerated — returns what
// it successfully pulled plus the latest error for the caller to log.
func (c *Client) GetMarketsTopByMcapMulti(ctx context.Context, totalLimit int) ([]MarketData, error) {
	if totalLimit <= 0 {
		totalLimit = 250
	}
	if totalLimit > 2000 {
		totalLimit = 2000
	}
	const pageSize = 250
	pages := (totalLimit + pageSize - 1) / pageSize
	all := make([]MarketData, 0, totalLimit)
	var lastErr error
	for p := 1; p <= pages; p++ {
		want := pageSize
		if p == pages && totalLimit%pageSize != 0 {
			want = totalLimit % pageSize
		}
		batch, err := c.getMarketsByMcapPage(ctx, want, p)
		if err != nil {
			lastErr = err
			continue
		}
		all = append(all, batch...)
		if len(batch) < want {
			break // end of catalog reached
		}
	}
	return all, lastErr
}

func (c *Client) getMarketsByMcapPage(ctx context.Context, limit, page int) ([]MarketData, error) {
	if limit <= 0 || limit > 250 {
		limit = 250
	}
	if page < 1 {
		page = 1
	}
	q := url.Values{
		"vs_currency": {"usd"},
		"order":       {"market_cap_desc"},
		"per_page":    {fmt.Sprintf("%d", limit)},
		"page":        {fmt.Sprintf("%d", page)},
	}
	body, err := c.do(ctx, "/coins/markets", q)
	if err != nil {
		return nil, err
	}
	var out []MarketData
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse /coins/markets (page %d): %w", page, err)
	}
	return out, nil
}

// GetMarkets fetches up-to-250 coins' market data in one request. ids is the
// CoinGecko slug list (NOT Binance symbols). vsCurrency typically "usd".
// Empty ids returns ([],nil) without an HTTP call (defensive).
func (c *Client) GetMarkets(ctx context.Context, ids []string, vsCurrency string) ([]MarketData, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 250 {
		return nil, fmt.Errorf("coingecko: ids batch >250 (%d) — caller must split", len(ids))
	}
	q := url.Values{
		"vs_currency": {vsCurrency},
		"ids":         {strings.Join(ids, ",")},
		"per_page":    {"250"},
		"page":        {"1"},
	}
	body, err := c.do(ctx, "/coins/markets", q)
	if err != nil {
		return nil, err
	}
	var out []MarketData
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse /coins/markets: %w", err)
	}
	return out, nil
}

// acquireRate blocks until the configured minInterval has elapsed since the
// last call. Serialises all CoinGecko requests across goroutines — at startup
// 2 collectors fire simultaneously and Demo plan returns 429 within seconds
// without this guard.
func (c *Client) acquireRate(ctx context.Context) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if c.minInterval > 0 && !c.lastCallAt.IsZero() {
		wait := c.minInterval - time.Since(c.lastCallAt)
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c.lastCallAt = time.Now()
	return nil
}

// do performs one Demo-API GET with automatic transient-error retry.
// Retries on 429 (rate limit) AND 403 ("Sorry, not able to access it right
// now" — CoinGecko returns this when a specific IP is temporarily blocked,
// distinct from rate limit). With proxy pool each retry samples a new IP,
// so usually the next attempt succeeds.
//
// Strategy: 3 attempts total. 1st 429 → wait 65s; subsequent or 403 →
// quick 3s retry (lets proxy rotate IP).
func (c *Client) do(ctx context.Context, path string, q url.Values) ([]byte, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, err := c.doOnce(ctx, path, q)
		if err == nil {
			return body, nil
		}
		lastErr = err
		var he *HTTPError
		isHTTP := errorsAs(err, &he)
		switch {
		case !isHTTP:
			// Network / SOCKS auth / DNS — retriable (proxy pool rotates IP).
		case he.HTTPCode == 429 || he.HTTPCode == 403:
			// Rate limit + per-IP block — retriable.
		default:
			// Other 4xx/5xx (5xx is rare for CoinGecko, 4xx like 401 invalid
			// key won't recover from retry).
			return nil, err
		}
		if attempt == maxAttempts {
			break
		}
		// 429 → long wait (rate window reset); 403 / network → short (rotate IP).
		wait := 3 * time.Second
		if isHTTP && he.HTTPCode == 429 {
			wait = 65 * time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// errorsAs is a thin shim so we don't have to import "errors" just for As.
func errorsAs(err error, target interface{}) bool {
	if he, ok := err.(*HTTPError); ok {
		if dst, ok := target.(**HTTPError); ok {
			*dst = he
			return true
		}
	}
	return false
}

func (c *Client) doOnce(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if err := c.acquireRate(ctx); err != nil {
		return nil, err
	}
	qLocal := url.Values{}
	for k, v := range q {
		qLocal[k] = v
	}
	if c.apiKey != "" {
		qLocal.Set("x_cg_demo_api_key", c.apiKey)
	}
	full := c.baseURL + path + "?" + qLocal.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("build req: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	httpClient := c.directHTTP
	if c.proxyHTTP != nil {
		pc, perr := c.proxyHTTP(ctx)
		if perr != nil {
			// Proxy unavailable → fall back to direct. Logged at caller via err
			// path eventually; non-fatal so Demo path still works.
			httpClient = c.directHTTP
		} else if pc != nil {
			httpClient = pc
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{HTTPCode: resp.StatusCode, Body: truncateBody(string(body))}
	}
	return body, nil
}

// truncateBody caps error-body excerpts so HTTPError doesn't carry MBs.
func truncateBody(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
