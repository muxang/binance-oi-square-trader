// T5 collector — tracks mark price of open positions. Reads trades table
// for status IN ('open','partial'), queries Binance premiumIndex per unique
// symbol, writes mark price to Redis (latest_price:{symbol}, TTL 5min per
// ARCH §7). Phase 1: 0 positions → silent return. Phase 4: auto-activates
// when first trade opens.
//
// Cron: */1 (60s, SPEC §T5 writes 30s — 1.10 acceptance reconciles SPEC).
// ref: SPEC §T5 + ARCH §7 latest_price + ARCH §9.5 retry pattern.

package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// PositionTradeQueries is the minimum DB surface T5 needs (CLAUDE.md §18 —
// accept interfaces in the consumer). *gen.Queries satisfies this implicitly.
type PositionTradeQueries interface {
	GetOpenTrades(ctx context.Context) ([]gen.GetOpenTradesRow, error)
}

// MarkPriceFetcher is the minimum binance surface T5 needs.
// *binance.Client satisfies this implicitly via FetchMarkPrice.
type MarkPriceFetcher interface {
	FetchMarkPrice(ctx context.Context, symbol string) (binance.MarkPriceData, error)
}

type PositionPriceConfig struct {
	PerTickTimeout   time.Duration
	PerSymbolTimeout time.Duration
	Concurrency      int
	RetryCount       int
	RetryInterval    time.Duration
	RedisTTL         time.Duration
}

type markPriceResult struct {
	symbol    string
	markPrice decimal.Decimal
	err       error
}

type PositionPriceCollector struct {
	fetcher MarkPriceFetcher
	queries PositionTradeQueries
	redis   *redis.Client
	log     zerolog.Logger
	cfg     PositionPriceConfig
	nowFunc func() time.Time
}

func NewPositionPriceCollector(fetcher MarkPriceFetcher, queries PositionTradeQueries, rdb *redis.Client, log zerolog.Logger, cfg PositionPriceConfig) *PositionPriceCollector {
	cfg = positionPriceDefaults(cfg)
	return &PositionPriceCollector{
		fetcher: fetcher,
		queries: queries,
		redis:   rdb,
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
	}
}

func positionPriceDefaults(cfg PositionPriceConfig) PositionPriceConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 25 * time.Second
	}
	if cfg.PerSymbolTimeout == 0 {
		cfg.PerSymbolTimeout = 8 * time.Second
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 5
	}
	if cfg.RetryCount == 0 {
		cfg.RetryCount = 2
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = 1 * time.Second
	}
	if cfg.RedisTTL == 0 {
		cfg.RedisTTL = 5 * time.Minute
	}
	return cfg
}

func (c *PositionPriceCollector) Name() string { return "position_price" }

func (c *PositionPriceCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	trades, err := c.queries.GetOpenTrades(tickCtx)
	if err != nil {
		return fmt.Errorf("get open trades: %w", err)
	}
	if len(trades) == 0 {
		return nil // 0 positions is the Phase 1 steady state — no log noise
	}

	symbols := uniqueSymbols(trades)
	results := c.fetchConcurrent(tickCtx, symbols)
	successCount := c.writeRedis(tickCtx, results)

	c.log.Info().
		Int("positions", len(trades)).
		Int("symbols", len(symbols)).
		Int("success", successCount).
		Msg("position_price tick complete")
	return nil
}

func uniqueSymbols(trades []gen.GetOpenTradesRow) []string {
	seen := make(map[string]struct{}, len(trades))
	out := make([]string, 0, len(trades))
	for _, t := range trades {
		if _, ok := seen[t.Symbol]; ok {
			continue
		}
		seen[t.Symbol] = struct{}{}
		out = append(out, t.Symbol)
	}
	return out
}

func (c *PositionPriceCollector) fetchConcurrent(ctx context.Context, symbols []string) []markPriceResult {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	results := make([]markPriceResult, len(symbols))
	for i, sym := range symbols {
		g.Go(func() error {
			d, err := c.fetchSingleMarkPrice(gctx, sym)
			results[i] = markPriceResult{symbol: sym, markPrice: d.MarkPrice, err: err}
			return nil // never propagate — keep peers running
		})
	}
	_ = g.Wait()
	return results
}

// fetchSingleMarkPrice wraps FetchMarkPrice with retry on 5xx/timeout/network.
// 4xx is not retried (parameter errors won't fix). RetryCount=2 → up to 3
// total attempts; 1s fixed interval.
func (c *PositionPriceCollector) fetchSingleMarkPrice(ctx context.Context, symbol string) (binance.MarkPriceData, error) {
	var lastErr error
	var data binance.MarkPriceData
	for attempt := 0; attempt <= c.cfg.RetryCount; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.cfg.RetryInterval):
			case <-ctx.Done():
				return data, ctx.Err()
			}
		}
		sCtx, cancel := context.WithTimeout(ctx, c.cfg.PerSymbolTimeout)
		d, err := c.fetcher.FetchMarkPrice(sCtx, symbol)
		cancel()
		if err == nil {
			return d, nil
		}
		var apiErr *binance.APIError
		if errors.As(err, &apiErr) && apiErr.HTTPCode >= 400 && apiErr.HTTPCode < 500 {
			return data, err
		}
		lastErr = err
		c.log.Warn().Err(err).Str("symbol", symbol).Int("attempt", attempt).Msg("mark_price retry")
	}
	return data, fmt.Errorf("after %d retries: %w", c.cfg.RetryCount, lastErr)
}

func (c *PositionPriceCollector) writeRedis(ctx context.Context, results []markPriceResult) int {
	pipe := c.redis.Pipeline()
	queued := []string{}
	for _, r := range results {
		if r.err != nil {
			continue
		}
		pipe.Set(ctx, "latest_price:"+r.symbol, r.markPrice.String(), c.cfg.RedisTTL)
		queued = append(queued, r.symbol)
	}
	if len(queued) == 0 {
		return 0
	}
	cmds, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		c.log.Error().Err(err).Msg("position_price: redis pipeline exec failed")
	}
	ok := 0
	for i, cmd := range cmds {
		if cmd.Err() == nil {
			ok++
		} else {
			c.log.Error().Err(cmd.Err()).Str("symbol", queued[i]).Msg("position_price: redis SET failed")
		}
	}
	return ok
}
