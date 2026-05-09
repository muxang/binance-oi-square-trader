package collector

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/pkg/indicator"
	"trader/internal/storage/postgres/gen"
)

// batchUpsertKlines flattens per-symbol bars into one params slice and
// delegates to writeFn (which chunks ≤5000 rows / pgx.Batch).
func (c *KlinesCollector) batchUpsertKlines(ctx context.Context, results []klinesResult) int {
	allParams := make([]gen.BatchUpsertKlinesParams, 0)
	successPerSymbol := 0
	for _, r := range results {
		if r.err != nil || len(r.bars) == 0 {
			continue
		}
		successPerSymbol++
		for _, b := range r.bars {
			allParams = append(allParams, gen.BatchUpsertKlinesParams{
				Symbol:      r.symbol,
				Timeframe:   c.cfg.KlineInterval,
				OpenTime:    b.OpenTime,
				Open:        b.Open,
				High:        b.High,
				Low:         b.Low,
				Close:       b.Close,
				Volume:      b.Volume,
				QuoteVolume: b.QuoteVolume,
			})
		}
	}
	rowsOK, err := c.writeFn(ctx, allParams)
	if err != nil {
		c.log.Error().Err(err).Int("attempted", len(allParams)).Int("rows_ok", rowsOK).Msg("klines DB write had errors")
	}
	if rowsOK == 0 {
		return 0
	}
	return successPerSymbol
}

// computeAndStoreIndicator pipelines per-symbol Redis writes in one
// round-trip. ATR / EMA share this; per-symbol errors are logged and skipped.
func (c *KlinesCollector) computeAndStoreIndicator(
	ctx context.Context, results []klinesResult, keyPrefix string, ttl time.Duration,
	compute func([]binance.KlineBar) (decimal.Decimal, error),
) int {
	pipe := c.redis.Pipeline()
	queued := []string{}
	for _, r := range results {
		if r.err != nil || len(r.bars) == 0 {
			continue
		}
		v, err := compute(r.bars)
		if err != nil {
			c.log.Error().Err(err).Str("symbol", r.symbol).Str("indicator", keyPrefix).Msg("indicator compute failed")
			continue
		}
		payload, _ := json.Marshal(indicatorPayload{Value: v.String(), ComputedAt: c.nowFunc().Format(time.RFC3339)})
		pipe.Set(ctx, keyPrefix+r.symbol, payload, ttl)
		queued = append(queued, r.symbol)
	}
	cmds, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		c.log.Error().Err(err).Str("indicator", keyPrefix).Msg("indicator pipeline exec failed")
	}
	ok := 0
	for i, cmd := range cmds {
		if cmd.Err() == nil {
			ok++
		} else {
			c.log.Error().Err(cmd.Err()).Str("symbol", queued[i]).Str("indicator", keyPrefix).Msg("indicator write failed")
		}
	}
	return ok
}

func (c *KlinesCollector) computeAndStoreATR(ctx context.Context, results []klinesResult) int {
	return c.computeAndStoreIndicator(ctx, results, "atr:", c.cfg.ATRRedisTTL,
		func(bars []binance.KlineBar) (decimal.Decimal, error) {
			h, l, cl := splitBars(bars)
			return indicator.ATR(h, l, cl, c.cfg.ATRPeriod)
		})
}

func (c *KlinesCollector) computeAndStoreEMA(ctx context.Context, results []klinesResult) int {
	return c.computeAndStoreIndicator(ctx, results, "ema20:", c.cfg.EMARedisTTL,
		func(bars []binance.KlineBar) (decimal.Decimal, error) {
			closes := make([]decimal.Decimal, len(bars))
			for i, b := range bars {
				closes[i] = b.Close
			}
			return indicator.EMA(closes, c.cfg.EMAPeriod)
		})
}

func splitBars(bars []binance.KlineBar) (highs, lows, closes []decimal.Decimal) {
	highs = make([]decimal.Decimal, len(bars))
	lows = make([]decimal.Decimal, len(bars))
	closes = make([]decimal.Decimal, len(bars))
	for i, b := range bars {
		highs[i] = b.High
		lows[i] = b.Low
		closes[i] = b.Close
	}
	return
}

// pgxWriteKlines chunks params into ≤5000-row pgx.Batch units (well under
// PG's 65535-param-per-query ceiling: 9 cols × 5000 = 45000). Per-row errors
// inside a chunk surface as firstErr; remaining chunks still execute.
func pgxWriteKlines(queries *gen.Queries) func(context.Context, []gen.BatchUpsertKlinesParams) (int, error) {
	return func(ctx context.Context, params []gen.BatchUpsertKlinesParams) (int, error) {
		if len(params) == 0 {
			return 0, nil
		}
		const chunkSize = 5000
		ok := 0
		var firstErr error
		for i := 0; i < len(params); i += chunkSize {
			end := i + chunkSize
			if end > len(params) {
				end = len(params)
			}
			r := queries.BatchUpsertKlines(ctx, params[i:end])
			r.Exec(func(_ int, err error) {
				if err == nil {
					ok++
				} else if firstErr == nil {
					firstErr = err
				}
			})
			r.Close()
		}
		return ok, firstErr
	}
}
