package collector

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// LargeHolderCollectorConfig parameterises R.11.A2c-1.
type LargeHolderCollectorConfig struct {
	Concurrency      int     // default 8 — same as OI collector
	HighFailureRate  float64 // default 0.30 — log Warn above this fraction
}

// LargeHolderCollector fetches each watchlist symbol's latest large-trader
// long/short ratio (both account-weighted and position-weighted) every 5min
// and persists to large_holder_ratios. Single tick = 2 BAPI calls per symbol
// (weight=0, separate 1000 req/5min/IP bucket).
//
// ref: references/user-snippets/contract-monitor.js (checkLargeHolderRatioWithData)
// ref: references/binance/urls.md §「Top Trader Long Short {Account,Position} Ratio」
type LargeHolderCollector struct {
	client       *binance.Client
	log          zerolog.Logger
	cfg          LargeHolderCollectorConfig
	watchlistFn  func(ctx context.Context) ([]string, error)
	upsertFn     func(ctx context.Context, arg gen.InsertLargeHolderRatioParams) error
}

// NewLargeHolderCollector wires the collector with pgx-backed reader/writer.
func NewLargeHolderCollector(client *binance.Client, pool *pgxpool.Pool, log zerolog.Logger, cfg LargeHolderCollectorConfig) *LargeHolderCollector {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 8
	}
	if cfg.HighFailureRate == 0 {
		cfg.HighFailureRate = 0.30
	}
	q := gen.New(pool)
	return &LargeHolderCollector{
		client:      client,
		log:         log,
		cfg:         cfg,
		watchlistFn: q.GetLatestWatchlistSymbols,
		upsertFn:    q.InsertLargeHolderRatio,
	}
}

func (c *LargeHolderCollector) Name() string { return "large_holder" }

// Run executes one 5min tick. Watchlist size × 2 BAPI calls = ~566 req for
// 283 symbols, well under the 1000 req/5min/IP limit for /futures/data/*.
func (c *LargeHolderCollector) Run(ctx context.Context) error {
	symbols, err := c.watchlistFn(ctx)
	if err != nil {
		return fmt.Errorf("watchlist: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New("large_holder: empty watchlist — upstream collector may be stale")
	}

	type result struct {
		symbol string
		params gen.InsertLargeHolderRatioParams
		err    error
	}
	results := make([]result, len(symbols))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	for i, sym := range symbols {
		g.Go(func() error {
			acct, posn, err := c.fetchOne(gctx, sym)
			results[i] = result{symbol: sym, params: c.buildParams(sym, acct, posn), err: err}
			return nil // never propagate — peer fetches keep running
		})
	}
	_ = g.Wait()

	successCount := 0
	for _, r := range results {
		if r.err != nil {
			c.log.Debug().Err(r.err).Str("symbol", r.symbol).Msg("large_holder: per-symbol fetch failed")
			continue
		}
		if err := c.upsertFn(ctx, r.params); err != nil {
			c.log.Warn().Err(err).Str("symbol", r.symbol).Msg("large_holder: upsert failed")
			continue
		}
		successCount++
	}

	failureRate := 1.0 - float64(successCount)/float64(len(symbols))
	if failureRate > c.cfg.HighFailureRate {
		c.log.Warn().Float64("failure_rate", failureRate).
			Int("total", len(symbols)).
			Int("success", successCount).
			Msg("large_holder: high failure rate")
	}
	if successCount == 0 {
		return errors.New("large_holder: all symbols failed — full-tick failure")
	}
	c.log.Info().Int("symbols", len(symbols)).Int("success", successCount).
		Float64("failure_rate", failureRate).Msg("large_holder tick complete")
	return nil
}

// fetchOne hits both endpoints sequentially per symbol (limit=1 = newest snapshot).
// Either endpoint's failure is tolerated — partial data is better than no data.
func (c *LargeHolderCollector) fetchOne(ctx context.Context, symbol string) (acct, posn *binance.LargeHolderRatio, err error) {
	aSlice, aErr := c.client.FetchTopLongShortAccountRatio(ctx, symbol, "5m", 1)
	pSlice, pErr := c.client.FetchTopLongShortPositionRatio(ctx, symbol, "5m", 1)
	if aErr != nil && pErr != nil {
		return nil, nil, fmt.Errorf("both endpoints failed: account=%v position=%v", aErr, pErr)
	}
	if len(aSlice) > 0 {
		acct = &aSlice[0]
	}
	if len(pSlice) > 0 {
		posn = &pSlice[0]
	}
	if acct == nil && posn == nil {
		return nil, nil, errors.New("both endpoints returned empty arrays")
	}
	return acct, posn, nil
}

// buildParams uses the newer timestamp between the two snapshots — both should
// align on the 5m boundary, but if one lags pick the freshest for the row's ts.
func (c *LargeHolderCollector) buildParams(symbol string, acct, posn *binance.LargeHolderRatio) gen.InsertLargeHolderRatioParams {
	p := gen.InsertLargeHolderRatioParams{Symbol: symbol, Ts: timez.NowUTC()}
	if acct != nil {
		p.Ts = acct.Timestamp
		p.AccountLongShortRatio = decimal.NullDecimal{Decimal: acct.LongShortRatio, Valid: true}
	}
	if posn != nil {
		if posn.Timestamp.After(p.Ts) {
			p.Ts = posn.Timestamp
		}
		p.PositionLongShortRatio = decimal.NullDecimal{Decimal: posn.LongShortRatio, Valid: true}
	}
	return p
}
