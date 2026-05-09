// T3 collector — hashtag engagement metrics for the monitoring pool. Reads
// watchlist from Redis, queries Square BAPI per symbol (with retries),
// writes (symbol, ts, content_count, view_count) into the time-series
// table. Cron: */5; empty watchlist skips (T4 may not be running yet —
// see ARCH §9.5 + SPEC §T3).

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"trader/internal/pkg/timez"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

const queryByHashtagPath = "/bapi/composite/v4/friendly/pgc/content/queryByHashtag"

type SquareHashtagConfig struct {
	PerTickTimeout    time.Duration
	PerSymbolTimeout  time.Duration
	Concurrency       int
	RetryCount        int
	RetryInterval     time.Duration
	HighFailureRate   float64
	WatchlistRedisKey string
}

type hashtagResult struct {
	symbol       string
	contentCount int64
	viewCount    int64
	err          error
}

// SquareHashtagCollector implements T3 — see file header. Empty watchlist
// is the expected state pre-1.6 (T4 not yet running) and during Redis
// outages: skip-with-warn rather than fall back to seed data, so a Redis
// failure can't silently feed wrong symbols into Phase 2 hot judgement.
type SquareHashtagCollector struct {
	client  *square.SquareClient
	redis   *redis.Client
	pool    *pgxpool.Pool
	queries *gen.Queries
	log     zerolog.Logger
	cfg     SquareHashtagConfig
	nowFunc func() time.Time
}

func NewSquareHashtagCollector(client *square.SquareClient, rdb *redis.Client, pool *pgxpool.Pool, log zerolog.Logger, cfg SquareHashtagConfig) *SquareHashtagCollector {
	cfg = squareHashtagDefaults(cfg)
	return &SquareHashtagCollector{
		client:  client,
		redis:   rdb,
		pool:    pool,
		queries: gen.New(pool),
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
	}
}

func squareHashtagDefaults(cfg SquareHashtagConfig) SquareHashtagConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 4 * time.Minute
	}
	if cfg.PerSymbolTimeout == 0 {
		cfg.PerSymbolTimeout = 8 * time.Second
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 10
	}
	if cfg.RetryCount == 0 {
		cfg.RetryCount = 2
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = 1 * time.Second
	}
	if cfg.HighFailureRate == 0 {
		cfg.HighFailureRate = 0.30
	}
	if cfg.WatchlistRedisKey == "" {
		cfg.WatchlistRedisKey = "watchlist:current"
	}
	return cfg
}

func (c *SquareHashtagCollector) Name() string { return "square_hashtag" }

func (c *SquareHashtagCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	symbols, err := c.readWatchlist(tickCtx)
	if err != nil {
		return fmt.Errorf("read watchlist: %w", err)
	}
	if len(symbols) == 0 {
		c.log.Warn().Msg("square_hashtag: watchlist empty — T3 skipped (T4 not running or redis issue)")
		return nil
	}

	results := c.fetchConcurrent(tickCtx, symbols)
	successCount := c.batchInsertHashtagHistory(tickCtx, results)
	failureRate := 1.0 - float64(successCount)/float64(len(symbols))

	c.log.Info().
		Int("symbols", len(symbols)).
		Int("success", successCount).
		Float64("failure_rate", failureRate).
		Msg("square_hashtag tick complete")
	if failureRate > c.cfg.HighFailureRate {
		c.log.Warn().Float64("failure_rate", failureRate).Msg("square_hashtag high failure rate")
	}
	return nil
}

// readWatchlist fetches the current pool from Redis. redis.Nil (key
// absent) returns empty slice, not error — callers gracefully skip.
func (c *SquareHashtagCollector) readWatchlist(ctx context.Context) ([]string, error) {
	val, err := c.redis.Get(ctx, c.cfg.WatchlistRedisKey).Result()
	if errors.Is(err, redis.Nil) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var symbols []string
	if err := json.Unmarshal([]byte(val), &symbols); err != nil {
		return nil, fmt.Errorf("unmarshal watchlist: %w", err)
	}
	return symbols, nil
}

func (c *SquareHashtagCollector) fetchConcurrent(ctx context.Context, symbols []string) []hashtagResult {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	results := make([]hashtagResult, len(symbols))
	for i, sym := range symbols {
		g.Go(func() error {
			cc, vc, err := c.fetchSingleHashtag(gctx, sym)
			results[i] = hashtagResult{symbol: sym, contentCount: cc, viewCount: vc, err: err}
			return nil // never propagate — keep peers running
		})
	}
	_ = g.Wait()
	return results
}

// fetchSingleHashtag wraps DoGet with retry on 5xx/timeout/network. 4xx
// is not retried (parameter errors won't fix). RetryCount=2 → up to 3
// total attempts; 1s fixed interval (per ARCH §9.5).
func (c *SquareHashtagCollector) fetchSingleHashtag(ctx context.Context, symbol string) (int64, int64, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.RetryCount; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.cfg.RetryInterval):
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			}
		}
		// hashtag = base symbol lowercased (BTCUSDT → btc). The '#' prefix
		// in square-discussion.py is string concat, not the BAPI param.
		hashtag := strings.ToLower(strings.TrimSuffix(symbol, "USDT"))
		params := url.Values{"hashtag": {hashtag}, "pageSize": {"1"}}

		sCtx, cancel := context.WithTimeout(ctx, c.cfg.PerSymbolTimeout)
		body, err := c.client.DoGet(sCtx, queryByHashtagPath, params)
		cancel()

		if err == nil {
			cc, vc, parseErr := parseHashtagResponse(body)
			if parseErr != nil {
				return 0, 0, fmt.Errorf("parse: %w", parseErr)
			}
			return cc, vc, nil
		}
		var sqErr *square.SquareError
		if errors.As(err, &sqErr) && sqErr.HTTPCode >= 400 && sqErr.HTTPCode < 500 {
			return 0, 0, err
		}
		lastErr = err
		c.log.Warn().Err(err).Str("symbol", symbol).Int("attempt", attempt).Msg("hashtag retry")
	}
	return 0, 0, fmt.Errorf("after %d retries: %w", c.cfg.RetryCount, lastErr)
}

// parseHashtagResponse extracts data.hashtag.contentCount + viewCount.
// 0 is a legal value (new hashtags may have no posts yet), so we use
// .Exists() to distinguish missing-field from zero-value.
func parseHashtagResponse(body []byte) (int64, int64, error) {
	cc := gjson.GetBytes(body, "data.hashtag.contentCount")
	vc := gjson.GetBytes(body, "data.hashtag.viewCount")
	if !cc.Exists() {
		return 0, 0, errors.New("contentCount field missing")
	}
	if !vc.Exists() {
		return 0, 0, errors.New("viewCount field missing")
	}
	return cc.Int(), vc.Int(), nil
}

func (c *SquareHashtagCollector) batchInsertHashtagHistory(ctx context.Context, results []hashtagResult) int {
	now := c.nowFunc()
	params := make([]gen.BatchInsertSquareHashtagParams, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			continue
		}
		params = append(params, gen.BatchInsertSquareHashtagParams{
			Symbol:       r.symbol,
			Ts:           now,
			ContentCount: r.contentCount,
			ViewCount:    r.viewCount,
		})
	}
	if len(params) == 0 {
		return 0
	}
	batch := c.queries.BatchInsertSquareHashtag(ctx, params)
	defer batch.Close()
	success := 0
	batch.Exec(func(idx int, err error) {
		if err != nil {
			c.log.Error().Err(err).Int("idx", idx).Msg("hashtag batch row failed")
			return
		}
		success++
	})
	return success
}
