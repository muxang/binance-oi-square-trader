// T3 collector — hashtag engagement metrics. Phase 2 v0.1 (commit 801d6e8):
// 全采集 ~530 USDT 永续 (15min cron), 通过 SymbolService.ListSymbols 拿全
// symbol, 服务于 §辅助信号 自适应 hot 判定 (新入池 symbol 立即拿到完整
// 24h 历史, 不再因数据不足永远 fallback)。Phase 1 池内 5min 模式已废弃,
// 不再依赖 watchlist:current。写 (symbol, ts, content_count, view_count)
// 进 square_hashtag_history 时序表, 走代理 + 并发 10 + 单币重试 2 次。
// 详见 SPEC.md §T3 + §辅助信号。

package collector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"trader/internal/pkg/timez"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

const queryByHashtagPath = "/bapi/composite/v4/friendly/pgc/content/queryByHashtag"

// HashtagSymbolLister is the minimum surface SquareHashtagCollector needs
// from SymbolService (CLAUDE.md §18 — accept interfaces in consumer).
// *binance.SymbolService implements this implicitly via duck typing.
type HashtagSymbolLister interface {
	ListSymbols(ctx context.Context) ([]string, error)
}

type SquareHashtagConfig struct {
	PerTickTimeout   time.Duration
	PerSymbolTimeout time.Duration
	Concurrency      int
	RetryCount       int
	RetryInterval    time.Duration
	HighFailureRate  float64
}

type hashtagResult struct {
	symbol       string
	contentCount int64
	viewCount    int64
	err          error
}

// SquareHashtagCollector implements T3. Phase 2 v0.1: 全采集 + 15min cron
// (see file header). symbolService 返空只在 SymbolService 异常时发生 —
// 不是正常状态, 视为 error 上抛而非 silently skip。
type SquareHashtagCollector struct {
	client        *square.SquareClient
	symbolService HashtagSymbolLister
	pool          *pgxpool.Pool
	queries       *gen.Queries
	log           zerolog.Logger
	cfg           SquareHashtagConfig
	nowFunc       func() time.Time
}

func NewSquareHashtagCollector(client *square.SquareClient, symbolService HashtagSymbolLister, pool *pgxpool.Pool, log zerolog.Logger, cfg SquareHashtagConfig) *SquareHashtagCollector {
	cfg = squareHashtagDefaults(cfg)
	return &SquareHashtagCollector{
		client:        client,
		symbolService: symbolService,
		pool:          pool,
		queries:       gen.New(pool),
		log:           log,
		cfg:           cfg,
		nowFunc:       timez.NowUTC,
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
	return cfg
}

func (c *SquareHashtagCollector) Name() string { return "square_hashtag" }

func (c *SquareHashtagCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	symbols, err := c.symbolService.ListSymbols(tickCtx)
	if err != nil {
		return fmt.Errorf("list symbols: %w", err)
	}
	if len(symbols) == 0 {
		// 全采集模式下 ListSymbols 返空 = SymbolService 异常 (cache 没刷或上游故障),
		// 不是正常状态 (Phase 1 watchlist 空才是正常),应 error 让 runner 计 metric。
		return errors.New("square_hashtag: SymbolService returned 0 symbols (cache miss or upstream error)")
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
