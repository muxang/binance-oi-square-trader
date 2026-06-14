// Uptrend collector — periodically scans top-N USDⓈ-M perps by 24h quote
// volume, applies the R.24 composite filter (see internal/market.CheckUptrend),
// and writes the snapshot to Redis for admin-api hot-path reads.
//
// Cost per cycle: 1 × ticker/24hr (weight 40) + 2·N × klines (weight 1 each,
// 1h + 4h per symbol) + 1 × BTC 4h klines.
// At N=200 cron '*/5 * * * *' ≈ 88 weight/min — under the 2400/min ceiling.
//
// Cadence: indicators only change on new closed bars (1h or 4h). 5min cron
// retries ≈12 times/hour against transient API errors.
//
// ref: references/binance/urls.md §「24hr Ticker」 / 「Kline / Candlestick」
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/market"
	"trader/internal/pkg/timez"
)

const (
	uptrendRedisKey = "admin:market:uptrend:v1"
	uptrendRedisTTL = 12 * time.Minute

	// R.29 appearance tracking: one ZSET per symbol; score=hour-bucket Unix
	// seconds, member=same string. ZADD is idempotent across multiple scans
	// in the same hour (member dedup), so the ZSET ends up storing exactly
	// one entry per hour the symbol passed finalSignal.
	uptrendPassHoursKeyPrefix = "admin:uptrend:pass_hours:"
	uptrendPassHistoryTTL     = 14 * 24 * time.Hour // wider than the 7d window for safety
)

type UptrendCollectorConfig struct {
	TopN          int
	Concurrency   int
	KlinesLimit   int // 1h klines per symbol; 100 → 99 closed (EMA50 + warmup)
	Klines4hLimit int // 4h klines per symbol; 50 → 49 closed (4h EMA20 + warmup)
	BTCSymbol     string
	FetchTimeout  time.Duration // per-symbol kline fetch budget
}

type UptrendCollector struct {
	client *binance.Client
	rdb    *redis.Client
	log    zerolog.Logger
	cfg    UptrendCollectorConfig
}

func NewUptrendCollector(c *binance.Client, rdb *redis.Client, log zerolog.Logger, cfg UptrendCollectorConfig) *UptrendCollector {
	if cfg.TopN == 0 {
		cfg.TopN = 200
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 20
	}
	if cfg.KlinesLimit == 0 {
		cfg.KlinesLimit = 100
	}
	if cfg.Klines4hLimit == 0 {
		cfg.Klines4hLimit = 50
	}
	if cfg.BTCSymbol == "" {
		cfg.BTCSymbol = "BTCUSDT"
	}
	if cfg.FetchTimeout == 0 {
		cfg.FetchTimeout = 10 * time.Second
	}
	return &UptrendCollector{client: c, rdb: rdb, log: log, cfg: cfg}
}

func (c *UptrendCollector) Name() string { return "uptrend" }

func (c *UptrendCollector) Run(ctx context.Context) error {
	tickers, err := c.client.FetchAll24hTicker(ctx)
	if err != nil {
		return fmt.Errorf("ticker24h: %w", err)
	}
	tickers = filterUSDTPerps(tickers)
	sort.Slice(tickers, func(i, j int) bool {
		return tickers[i].QuoteVolume.GreaterThan(tickers[j].QuoteVolume)
	})
	if len(tickers) > c.cfg.TopN {
		tickers = tickers[:c.cfg.TopN]
	}

	btcClose, btcPct4h, err := c.fetchBTC4hChange(ctx)
	if err != nil {
		return fmt.Errorf("btc 4h ref: %w", err)
	}
	c.log.Debug().Float64("btc_close", btcClose).Float64("btc_4h_pct", btcPct4h).
		Int("candidates", len(tickers)).Msg("uptrend: starting scan")

	items := c.scanConcurrent(ctx, tickers, btcPct4h)
	sort.Slice(items, func(i, j int) bool { return items[i].RelStrength > items[j].RelStrength })

	// R.29: record passes + enrich each item with IsNewThisHour / PassCount7d.
	// Pipelined: 1 round-trip for adds, 1 for counts. Cost is negligible vs
	// the ~200 kline fetches that dominate scan latency.
	c.recordPassAndEnrich(ctx, items)

	if c.rdb == nil {
		return nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := c.rdb.Set(ctx, uptrendRedisKey, b, uptrendRedisTTL).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	passing := 0
	for _, it := range items {
		if it.Pass {
			passing++
		}
	}
	c.log.Info().Int("evaluated", len(items)).Int("passing", passing).
		Float64("btc_4h_pct", btcPct4h).Msg("uptrend: scan complete")
	return nil
}

// recordPassAndEnrich is R.29's appearance-tracking pass.
//
//	Step A: for each passing symbol, ZADD this-hour to its history zset and
//	        bump the TTL so cleanup is automatic. ZADD is idempotent on
//	        member, so the multiple 5min scans within one hour collapse to
//	        a single entry.
//	Step B: pipeline ZCOUNT(last 7d) for every item → PassCount7d.
//	        For passing items also ZCOUNT(previous hour) → IsNewThisHour iff
//	        the previous-hour count is zero.
//
// Errors are non-fatal: a Redis blip just leaves the new R.29 fields at zero
// for this tick; the cached UptrendItem still has all the indicator data.
func (c *UptrendCollector) recordPassAndEnrich(ctx context.Context, items []market.UptrendItem) {
	if c.rdb == nil || len(items) == 0 {
		return
	}
	hourNow := timez.NowUTC().Truncate(time.Hour).Unix()
	sevenDaysAgo := hourNow - 7*24*3600
	prevHourStart := hourNow - 3600
	prevHourEnd := hourNow - 1
	hourNowStr := strconv.FormatInt(hourNow, 10)

	// Step A — record passes.
	for _, it := range items {
		if !it.Pass {
			continue
		}
		key := uptrendPassHoursKeyPrefix + it.Symbol
		// Idempotent: same hour bucket member ⇒ no-op on later scans this hour.
		if err := c.rdb.ZAdd(ctx, key, redis.Z{Score: float64(hourNow), Member: hourNowStr}).Err(); err != nil {
			c.log.Debug().Err(err).Str("symbol", it.Symbol).Msg("uptrend.history: ZAdd failed (non-fatal)")
			continue
		}
		_ = c.rdb.Expire(ctx, key, uptrendPassHistoryTTL).Err()
	}

	// Step B — pipelined counts. ZRANGEBYSCORE strings: redis tolerates float
	// score as integer Unix seconds; we pass strings to be unambiguous.
	pipe := c.rdb.Pipeline()
	count7dCmds := make([]*redis.IntCmd, len(items))
	prevHourCmds := make([]*redis.IntCmd, len(items)) // only populated for passing items
	sevenStr := strconv.FormatInt(sevenDaysAgo, 10)
	prevStartStr := strconv.FormatInt(prevHourStart, 10)
	prevEndStr := strconv.FormatInt(prevHourEnd, 10)
	for i, it := range items {
		key := uptrendPassHoursKeyPrefix + it.Symbol
		count7dCmds[i] = pipe.ZCount(ctx, key, sevenStr, "+inf")
		if it.Pass {
			prevHourCmds[i] = pipe.ZCount(ctx, key, prevStartStr, prevEndStr)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		c.log.Debug().Err(err).Msg("uptrend.history: pipeline exec failed (non-fatal)")
		return
	}
	for i := range items {
		items[i].PassCount7d = int(count7dCmds[i].Val())
		if items[i].Pass && prevHourCmds[i] != nil {
			items[i].IsNewThisHour = prevHourCmds[i].Val() == 0
		}
	}
}

// USDT-margined perps only (drops SYMBOLUSDC / SYMBOL_240329 quarterlies etc).
func filterUSDTPerps(in []binance.Ticker24hData) []binance.Ticker24hData {
	out := make([]binance.Ticker24hData, 0, len(in))
	for _, t := range in {
		if strings.HasSuffix(t.Symbol, "USDT") && !strings.Contains(t.Symbol, "_") {
			out = append(out, t)
		}
	}
	return out
}

// fetchBTC4hChange returns the latest closed BTCUSDT 4h close and its single-bar
// pct change: (close − open) / open. Used as the reference for relativeStrength.
func (c *UptrendCollector) fetchBTC4hChange(ctx context.Context) (latestClose, pct4h float64, err error) {
	bctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	bars, err := c.fetchKlines(bctx, c.cfg.BTCSymbol, "4h", c.cfg.Klines4hLimit)
	if err != nil {
		return 0, 0, err
	}
	bars = dropIncompleteBar(bars)
	if len(bars) < 1 {
		return 0, 0, fmt.Errorf("btc 4h bars=%d", len(bars))
	}
	return latestBarChange(bars)
}

func (c *UptrendCollector) scanConcurrent(ctx context.Context, ts []binance.Ticker24hData, btcPct4h float64) []market.UptrendItem {
	now := timez.NowUTC()
	var (
		mu    sync.Mutex
		items = make([]market.UptrendItem, 0, len(ts))
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	for _, t := range ts {
		sym := t.Symbol
		g.Go(func() error {
			bctx, cancel := context.WithTimeout(gctx, c.cfg.FetchTimeout)
			defer cancel()

			bars1h, err := c.fetchKlines(bctx, sym, "1h", c.cfg.KlinesLimit)
			if err != nil {
				c.log.Debug().Str("symbol", sym).Err(err).Msg("uptrend: 1h klines fetch failed")
				return nil
			}
			bars1h = dropIncompleteBar(bars1h)

			bars4h, err := c.fetchKlines(bctx, sym, "4h", c.cfg.Klines4hLimit)
			if err != nil {
				c.log.Debug().Str("symbol", sym).Err(err).Msg("uptrend: 4h klines fetch failed")
				return nil
			}
			bars4h = dropIncompleteBar(bars4h)
			if len(bars4h) < 1 {
				return nil
			}
			_, tokenPct4h, err := latestBarChange(bars4h)
			if err != nil {
				return nil
			}

			closes1h, highs, lows, vols := ohlcvAsFloats(bars1h)
			closes4h := closesAsFloats(bars4h)

			item, err := market.CheckUptrend(sym, closes1h, highs, lows, vols, closes4h, tokenPct4h, btcPct4h)
			if err != nil {
				// per-symbol skip: insufficient data / NaN / VolumeMA20≤0
				return nil
			}
			item.TriggerTime = now

			mu.Lock()
			items = append(items, item)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return items
}

func (c *UptrendCollector) fetchKlines(ctx context.Context, symbol, interval string, limit int) ([]binance.KlineBar, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	body, err := c.client.DoRead(ctx, "/fapi/v1/klines", params, 1)
	if err != nil {
		return nil, err
	}
	return binance.ParseKlines(body)
}

// latestBarChange returns (close, pct_change) of the latest bar where
// pct_change = (close − open) / open. Caller pre-strips incomplete bar.
func latestBarChange(bars []binance.KlineBar) (closePx, pct float64, err error) {
	if len(bars) < 1 {
		return 0, 0, errors.New("latestBarChange: empty bars")
	}
	latest := bars[len(bars)-1]
	open, _ := latest.Open.Float64()
	closePx, _ = latest.Close.Float64()
	if open == 0 {
		return 0, 0, errors.New("latestBarChange: open is zero")
	}
	pct = (closePx - open) / open
	return closePx, pct, nil
}

// dropIncompleteBar removes the last (in-progress) kline.
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Kline-Candlestick-Data
func dropIncompleteBar(bars []binance.KlineBar) []binance.KlineBar {
	if len(bars) <= 1 {
		return bars
	}
	return bars[:len(bars)-1]
}

func closesAsFloats(bars []binance.KlineBar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i], _ = b.Close.Float64()
	}
	return out
}

// ohlcvAsFloats — volume slice prefers QuoteVolume (USDT-denominated,
// cross-token comparable). Falls back to base Volume per spec §八.7 when
// quote unavailable. Other fields are direct from KlineBar.
func ohlcvAsFloats(bars []binance.KlineBar) (closes, highs, lows, volumes []float64) {
	n := len(bars)
	closes = make([]float64, n)
	highs = make([]float64, n)
	lows = make([]float64, n)
	volumes = make([]float64, n)
	for i, b := range bars {
		closes[i], _ = b.Close.Float64()
		highs[i], _ = b.High.Float64()
		lows[i], _ = b.Low.Float64()
		qv, _ := b.QuoteVolume.Float64()
		if qv > 0 {
			volumes[i] = qv
		} else {
			volumes[i], _ = b.Volume.Float64()
		}
	}
	return
}
