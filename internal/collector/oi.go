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
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// OICollectorConfig parameterises T1.
type OICollectorConfig struct {
	Concurrency     int           // default 8
	SymbolCacheTTL  time.Duration // default 1h — exchangeInfo refresh interval
	OIHistLimit     int           // default 10 — periods per symbol per tick
	HighFailureRate float64       // default 0.30 — log Warn above this fraction
}

// OIPoint is one parsed OI sample at a Binance-aligned 5-min boundary.
type OIPoint struct {
	Symbol     string
	TS         time.Time
	OI         decimal.Decimal
	OIValueUSD decimal.Decimal
}

type oiResult struct {
	symbol string
	points []OIPoint
	err    error
}

// OICollector implements T1: every 5 minutes, fetch the open-interest history
// for every USDT-margined perpetual and append to oi_history.
type OICollector struct {
	client *binance.Client
	log    zerolog.Logger
	cfg    OICollectorConfig

	symbolsMu sync.Mutex
	symbols   []string
	symbolsAt time.Time

	nowFunc func() time.Time
	writeFn func(ctx context.Context, points []OIPoint) (rowsWritten int, err error)
}

// NewOICollector wires T1 with sensible defaults and the pgx-backed writer.
func NewOICollector(client *binance.Client, pool *pgxpool.Pool, log zerolog.Logger, cfg OICollectorConfig) *OICollector {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 8
	}
	if cfg.SymbolCacheTTL == 0 {
		cfg.SymbolCacheTTL = time.Hour
	}
	if cfg.OIHistLimit == 0 {
		cfg.OIHistLimit = 10
	}
	if cfg.HighFailureRate == 0 {
		cfg.HighFailureRate = 0.30
	}
	queries := gen.New(pool)
	return &OICollector{
		client:  client,
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
		writeFn: pgxWriteOI(queries),
	}
}

func (c *OICollector) Name() string { return "oi" }

// Run executes one tick: refresh symbol list (cache TTL), fetch OI per symbol
// concurrently, batch-insert successes. Returns error only when symbol list
// is unfetchable or every per-symbol fetch fails (full-tick failure).
func (c *OICollector) Run(ctx context.Context) error {
	symbols, err := c.fetchSymbols(ctx)
	if err != nil {
		return fmt.Errorf("fetchSymbols: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New("oi: no USDT-perp symbols from exchangeInfo")
	}

	results := c.fetchOIConcurrent(ctx, symbols)

	allPoints := make([]OIPoint, 0, len(symbols)*c.cfg.OIHistLimit)
	successSymbols := 0
	for _, r := range results {
		if r.err != nil {
			c.log.Warn().Err(r.err).Str("symbol", r.symbol).Msg("oi fetch failed")
			continue
		}
		successSymbols++
		allPoints = append(allPoints, r.points...)
	}

	failureRate := 1.0 - float64(successSymbols)/float64(len(symbols))
	if failureRate > c.cfg.HighFailureRate {
		c.log.Warn().
			Float64("failure_rate", failureRate).
			Int("total", len(symbols)).
			Int("success", successSymbols).
			Msg("oi collector high failure rate")
	}
	if successSymbols == 0 {
		return errors.New("oi: all symbol fetches failed — full-tick failure")
	}

	rowsWritten, err := c.writeFn(ctx, allPoints)
	if err != nil {
		c.log.Error().Err(err).Int("attempted", len(allPoints)).Msg("oi write failed")
	}

	c.log.Info().
		Int("symbols", len(symbols)).
		Int("success_symbols", successSymbols).
		Int("rows_written", rowsWritten).
		Float64("failure_rate", failureRate).
		Msg("oi tick complete")
	return nil
}

type exchangeInfoResp struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		ContractType string `json:"contractType"`
		Status       string `json:"status"`
		QuoteAsset   string `json:"quoteAsset"`
		MarginAsset  string `json:"marginAsset"`
	} `json:"symbols"`
}

// fetchSymbols returns USDT-margined perpetuals currently TRADING. Cached for
// SymbolCacheTTL (default 1h). Returns a copy; the cached slice is never
// shared with callers, so mutation is safe.
func (c *OICollector) fetchSymbols(ctx context.Context) ([]string, error) {
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

// fetchOIConcurrent fans out fetchSingleOI across cfg.Concurrency goroutines.
// Per-symbol failures land in oiResult.err; errgroup is configured so a
// single failure never cancels its peers.
func (c *OICollector) fetchOIConcurrent(ctx context.Context, symbols []string) []oiResult {
	results := make([]oiResult, len(symbols))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	for i, sym := range symbols {
		g.Go(func() error {
			points, err := c.fetchSingleOI(gctx, sym)
			results[i] = oiResult{symbol: sym, points: points, err: err}
			return nil // never propagate — keep peers running
		})
	}
	_ = g.Wait()
	return results
}

type oiHistEntry struct {
	Symbol               string      `json:"symbol"`
	SumOpenInterest      string      `json:"sumOpenInterest"`
	SumOpenInterestValue string      `json:"sumOpenInterestValue"`
	Timestamp            json.Number `json:"timestamp"` // doc says STRING; live API returns number — json.Number handles both
}

func (c *OICollector) fetchSingleOI(ctx context.Context, symbol string) ([]OIPoint, error) {
	params := url.Values{
		"symbol": {symbol},
		"period": {"5m"},
		"limit":  {strconv.Itoa(c.cfg.OIHistLimit)},
	}
	// weight=0: openInterestHist doesn't count against the IP REQUEST_WEIGHT
	// bucket — but it has its own 1000 req/5min/IP limit (see ARCHITECTURE
	// Phase 1 ops note about OIConcurrency × 5min / proxy_pool_size).
	body, err := c.client.DoRead(ctx, "/futures/data/openInterestHist", params, 0)
	if err != nil {
		return nil, err
	}
	var entries []oiHistEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	points := make([]OIPoint, 0, len(entries))
	for _, e := range entries {
		oi, err := decimal.NewFromString(e.SumOpenInterest)
		if err != nil {
			return nil, fmt.Errorf("parse oi: %w", err)
		}
		oiVal, err := decimal.NewFromString(e.SumOpenInterestValue)
		if err != nil {
			return nil, fmt.Errorf("parse oi_value: %w", err)
		}
		ms, err := e.Timestamp.Int64()
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		points = append(points, OIPoint{
			Symbol:     symbol,
			TS:         time.UnixMilli(ms).UTC(),
			OI:         oi,
			OIValueUSD: oiVal,
		})
	}
	return points, nil
}

// pgxWriteOI returns a writeFn that batches every point into one sqlc
// :batchexec round-trip. ON CONFLICT DO NOTHING absorbs duplicate rows from
// the limit-10-revisits-recent-ticks pattern.
func pgxWriteOI(queries *gen.Queries) func(context.Context, []OIPoint) (int, error) {
	return func(ctx context.Context, points []OIPoint) (int, error) {
		if len(points) == 0 {
			return 0, nil
		}
		params := make([]gen.InsertOIHistoryParams, len(points))
		for i, p := range points {
			params[i] = gen.InsertOIHistoryParams{
				Symbol:     p.Symbol,
				Ts:         p.TS,
				Oi:         p.OI,
				OiValueUsd: p.OIValueUSD,
			}
		}
		results := queries.InsertOIHistory(ctx, params)
		defer results.Close()
		var firstErr error
		ok := 0
		results.Exec(func(_ int, err error) {
			if err == nil {
				ok++
			} else if firstErr == nil {
				firstErr = err
			}
		})
		return ok, firstErr
	}
}
