// R.31: stock-backed perpetuals collector — hourly fetch /fapi/v1/exchangeInfo
// and writes the set of symbols where underlyingType="EQUITY" to Redis.
// Admin-api reads the key; frontend SymbolLink shows a "📈" badge when a symbol
// is member of the set.
//
// Binance taxonomy (verified via testnet exchangeInfo 2026-06-18):
//   crypto perp:  underlyingType="COIN",  underlyingSubType=[],         contractType="PERPETUAL"
//   stock perp:   underlyingType="EQUITY", underlyingSubType=["TradFi"], contractType="TRADIFI_PERPETUAL"
//
// Using EQUITY as the canonical classifier — single field, future-proof against
// new sub-categories Binance might add (e.g. ["ETF"], ["Forex"]).
//
// ref: references/binance/urls.md §「Exchange Information」 GET /fapi/v1/exchangeInfo
// fetched: 2026-06-18
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"trader/internal/binance"
)

const (
	stockSymbolsRedisKey = "admin:stock:symbols:v1"
	// 2h TTL covers two missed crons before the frontend sees empty.
	// Exchange info changes very rarely (new listings), so staleness up to
	// 2h is acceptable.
	stockSymbolsRedisTTL = 2 * time.Hour
)

type StockSymbolsCollector struct {
	client *binance.Client
	rdb    *redis.Client
	log    zerolog.Logger
}

func NewStockSymbolsCollector(c *binance.Client, rdb *redis.Client, log zerolog.Logger) *StockSymbolsCollector {
	return &StockSymbolsCollector{client: c, rdb: rdb, log: log}
}

func (c *StockSymbolsCollector) Name() string { return "stock_symbols" }

// Run fetches exchangeInfo, filters underlyingType=EQUITY + status=TRADING,
// writes the sorted JSON array to Redis.
func (c *StockSymbolsCollector) Run(ctx context.Context) error {
	body, err := c.client.DoRead(ctx, "/fapi/v1/exchangeInfo", url.Values{}, 1)
	if err != nil {
		return fmt.Errorf("exchangeInfo: %w", err)
	}
	var resp struct {
		Symbols []struct {
			Symbol         string `json:"symbol"`
			Status         string `json:"status"`
			UnderlyingType string `json:"underlyingType"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse exchangeInfo: %w", err)
	}
	stocks := make([]string, 0, 32)
	for _, s := range resp.Symbols {
		if strings.EqualFold(s.UnderlyingType, "EQUITY") && strings.EqualFold(s.Status, "TRADING") {
			stocks = append(stocks, s.Symbol)
		}
	}
	sort.Strings(stocks)

	if c.rdb == nil {
		c.log.Info().Int("count", len(stocks)).Msg("stock_symbols: (no redis) refresh skipped")
		return nil
	}
	b, err := json.Marshal(stocks)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := c.rdb.Set(ctx, stockSymbolsRedisKey, b, stockSymbolsRedisTTL).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	c.log.Info().Int("count", len(stocks)).Msg("stock_symbols: refreshed")
	return nil
}
