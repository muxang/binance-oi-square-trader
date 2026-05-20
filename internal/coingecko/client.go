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

// Client is goroutine-safe; share one instance across all collectors.
type Client struct {
	baseURL string
	apiKey  string // Demo key; empty = unauthenticated (slower rate, still works)
	http    *http.Client
}

// NewClient constructs a Demo-API client. apiKey may be "" for unauthenticated
// access (still works, lower rate limits). 15s timeout matches Square client
// style; CoinGecko Demo p99 is well under that even on slow days.
func NewClient(apiKey string) *Client {
	return &Client{
		baseURL: DemoBaseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// withCustomBase exists for tests; production callers use NewClient.
func (c *Client) withCustomBase(base string) *Client {
	c.baseURL = base
	return c
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

// do performs one Demo-API GET. Auth (when apiKey set) is via query param —
// CoinGecko's documented method for the demo tier.
func (c *Client) do(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if c.apiKey != "" {
		q.Set("x_cg_demo_api_key", c.apiKey)
	}
	full := c.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("build req: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
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
