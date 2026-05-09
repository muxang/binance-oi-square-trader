package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// KlinesCollectorConfig parameterises T7. Zero values pick sensible defaults.
type KlinesCollectorConfig struct {
	Concurrency     int
	SymbolCacheTTL  time.Duration
	KlineLimit      int
	KlineInterval   string
	ATRPeriod       int
	EMAPeriod       int
	ATRRedisTTL     time.Duration
	EMARedisTTL     time.Duration
	HighFailureRate float64
}

type klinesResult struct {
	symbol string
	bars   []binance.KlineBar
	err    error
}

// indicatorPayload is the JSON written to Redis under atr:{symbol} /
// ema20:{symbol}. Value is the decimal as a string so no float64 round-trip
// happens on the money-safety path (CLAUDE.md §19).
type indicatorPayload struct {
	Value      string `json:"value"`
	ComputedAt string `json:"computed_at"`
}

// KlinesCollector implements T7: every 5 minutes, fetch 30 × 15m klines for
// each USDT-perp symbol, persist OHLCV to klines (hypertable), and compute
// ATR(14) + EMA(20) into Redis. The three writes (DB, ATR, EMA) live in
// klines_writers.go and are independent — any one failing does not block
// the others.
type KlinesCollector struct {
	client    *binance.Client
	redis     *redis.Client
	log       zerolog.Logger
	cfg       KlinesCollectorConfig
	symbolsMu sync.Mutex
	symbols   []string
	symbolsAt time.Time
	nowFunc   func() time.Time
	writeFn   func(ctx context.Context, params []gen.BatchUpsertKlinesParams) (int, error)
}

func NewKlinesCollector(client *binance.Client, pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger, cfg KlinesCollectorConfig) *KlinesCollector {
	cfg = klinesDefaults(cfg)
	return &KlinesCollector{
		client:  client,
		redis:   rdb,
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
		writeFn: pgxWriteKlines(gen.New(pool)),
	}
}

func klinesDefaults(cfg KlinesCollectorConfig) KlinesCollectorConfig {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 8
	}
	if cfg.SymbolCacheTTL == 0 {
		cfg.SymbolCacheTTL = time.Hour
	}
	if cfg.KlineLimit == 0 {
		cfg.KlineLimit = 30
	}
	if cfg.KlineInterval == "" {
		cfg.KlineInterval = "15m"
	}
	if cfg.ATRPeriod == 0 {
		cfg.ATRPeriod = 14
	}
	if cfg.EMAPeriod == 0 {
		cfg.EMAPeriod = 20
	}
	if cfg.ATRRedisTTL == 0 {
		cfg.ATRRedisTTL = 30 * time.Minute
	}
	if cfg.EMARedisTTL == 0 {
		cfg.EMARedisTTL = 30 * time.Minute
	}
	if cfg.HighFailureRate == 0 {
		cfg.HighFailureRate = 0.30
	}
	return cfg
}

func (c *KlinesCollector) Name() string { return "klines" }

func (c *KlinesCollector) Run(ctx context.Context) error {
	symbols, err := c.fetchSymbols(ctx)
	if err != nil {
		return fmt.Errorf("fetchSymbols: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New("klines: no USDT-perp symbols from exchangeInfo")
	}
	results := c.fetchKlinesConcurrent(ctx, symbols)

	successDB := c.batchUpsertKlines(ctx, results)
	successATR := c.computeAndStoreATR(ctx, results)
	successEMA := c.computeAndStoreEMA(ctx, results)

	failureRate := 1.0 - float64(successDB)/float64(len(symbols))
	c.log.Info().
		Int("symbols", len(symbols)).
		Int("success_db", successDB).
		Int("success_atr", successATR).
		Int("success_ema", successEMA).
		Float64("failure_rate", failureRate).
		Msg("klines tick complete")
	if failureRate > c.cfg.HighFailureRate {
		c.log.Warn().Float64("failure_rate", failureRate).Msg("klines collector high failure rate")
	}
	if successDB == 0 {
		return errors.New("klines: all symbol DB writes failed — full-tick failure")
	}
	return nil
}

// fetchSymbols mirrors OICollector.fetchSymbols. exchangeInfoResp lives in
// oi.go (same package) — reused, not redefined. Cached for SymbolCacheTTL.
func (c *KlinesCollector) fetchSymbols(ctx context.Context) ([]string, error) {
	c.symbolsMu.Lock()
	if len(c.symbols) > 0 && c.nowFunc().Sub(c.symbolsAt) < c.cfg.SymbolCacheTTL {
		out := append([]string{}, c.symbols...)
		c.symbolsMu.Unlock()
		return out, nil
	}
	c.symbolsMu.Unlock()

	body, err := c.client.DoRead(ctx, "/fapi/v1/exchangeInfo", nil, 1)
	if err != nil {
		return nil, fmt.Errorf("exchangeInfo: %w", err)
	}
	var resp exchangeInfoResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("exchangeInfo parse: %w", err)
	}
	out := make([]string, 0, len(resp.Symbols))
	for _, s := range resp.Symbols {
		if s.ContractType == "PERPETUAL" && s.QuoteAsset == "USDT" && s.MarginAsset == "USDT" && s.Status == "TRADING" {
			out = append(out, s.Symbol)
		}
	}
	c.symbolsMu.Lock()
	c.symbols = out
	c.symbolsAt = c.nowFunc()
	c.symbolsMu.Unlock()
	return append([]string{}, out...), nil
}

func (c *KlinesCollector) fetchKlinesConcurrent(ctx context.Context, symbols []string) []klinesResult {
	results := make([]klinesResult, len(symbols))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	for i, sym := range symbols {
		g.Go(func() error {
			bars, err := c.fetchSingleKline(gctx, sym)
			results[i] = klinesResult{symbol: sym, bars: bars, err: err}
			return nil // never propagate — keep peers running
		})
	}
	_ = g.Wait()
	return results
}

func (c *KlinesCollector) fetchSingleKline(ctx context.Context, symbol string) ([]binance.KlineBar, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {c.cfg.KlineInterval},
		"limit":    {strconv.Itoa(c.cfg.KlineLimit)},
	}
	body, err := c.client.DoRead(ctx, "/fapi/v1/klines", params, 1)
	if err != nil {
		return nil, err
	}
	return binance.ParseKlines(body)
}
