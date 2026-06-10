// Binance Alpha token list collector — fetches the public Alpha trading list
// hourly and writes the set of base symbols (uppercase) to Redis. Admin-api
// reads the key; frontend SymbolLink shows an "α" badge when a symbol is
// member of the set.
//
// Endpoint host is www.binance.com (NOT fapi.binance.com), so we bypass
// binance.Client.DoRead (which is fapi-bound) and call the proxy-rotated
// HTTPClient directly.
//
// ref: https://developers.binance.com/docs/alpha/market-data/rest-api/token-list
// docs: returns Alpha token list; .data[i].symbol is base asset (e.g. "gorilla").
// fetched: 2026-06-10
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"trader/internal/binance"
)

const (
	alphaTokenListURL = "https://www.binance.com/bapi/defi/v1/public/wallet-direct/buw/wallet/cex/alpha/all/token/list"
	alphaRedisKey     = "admin:alpha:symbols:v1"
	// 2h TTL covers two cron misses before frontend sees an empty set.
	// Alpha list changes ~daily at most, so staleness up to 2h is acceptable.
	alphaRedisTTL  = 2 * time.Hour
	alphaTimeoutS  = 15
)

type AlphaCollector struct {
	proxy binance.ProxyManager
	rdb   *redis.Client
	log   zerolog.Logger
}

func NewAlphaCollector(proxy binance.ProxyManager, rdb *redis.Client, log zerolog.Logger) *AlphaCollector {
	return &AlphaCollector{proxy: proxy, rdb: rdb, log: log}
}

func (c *AlphaCollector) Name() string { return "alpha" }

// Run fetches the Alpha token list, normalizes base symbols to {BASE}USDT
// (uppercase) for direct perp-symbol matching, and writes the JSON array to
// Redis. Symbols whose corresponding USDT perp does not exist are still
// included — the frontend membership check is a no-op for them.
func (c *AlphaCollector) Run(ctx context.Context) error {
	symbols, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	if c.rdb == nil {
		return nil
	}
	b, err := json.Marshal(symbols)
	if err != nil {
		return fmt.Errorf("alpha marshal: %w", err)
	}
	if err := c.rdb.Set(ctx, alphaRedisKey, b, alphaRedisTTL).Err(); err != nil {
		return fmt.Errorf("alpha redis set: %w", err)
	}
	c.log.Info().Int("count", len(symbols)).Msg("alpha: token list refreshed")
	return nil
}

func (c *AlphaCollector) fetch(ctx context.Context) ([]string, error) {
	hc, _, err := c.proxy.HTTPClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("alpha proxy: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, alphaTimeoutS*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, alphaTokenListURL, nil)
	if err != nil {
		return nil, fmt.Errorf("alpha req: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (trader/uptrend)")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alpha http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("alpha http %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Code    string `json:"code"`
		Success bool   `json:"success"`
		Data    []struct {
			Symbol  string `json:"symbol"`
			AlphaID string `json:"alphaId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("alpha parse: %w", err)
	}
	if !payload.Success || payload.Code != "000000" {
		return nil, fmt.Errorf("alpha api code=%s success=%v", payload.Code, payload.Success)
	}
	seen := make(map[string]struct{}, len(payload.Data))
	out := make([]string, 0, len(payload.Data))
	for _, t := range payload.Data {
		base := strings.ToUpper(strings.TrimSpace(t.Symbol))
		if base == "" {
			continue
		}
		// Match against USDⓈ-M perp naming: base + USDT.
		perp := base + "USDT"
		if _, dup := seen[perp]; dup {
			continue
		}
		seen[perp] = struct{}{}
		out = append(out, perp)
	}
	return out, nil
}
