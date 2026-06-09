// Uptrend collector — periodically scans top-N USDⓈ-M perps by 24h quote
// volume, applies the 6-rule trend filter (see internal/market.CheckUptrend),
// and writes the snapshot to Redis for admin-api hot-path reads.
//
// Cost per cycle:  1 × ticker/24hr (weight 40) + (N+1) × klines (weight 1).
// At N=200 cron '*/5 * * * *' ≈ 48 weight/min — well under the 2400/min
// IP-shared futures market-data ceiling.
//
// Cadence note: indicators only change on new 1h candle close. 5min cron
// gives ~12 retries per hour against transient API errors without wasting
// the 30 redundant scans/hour the previous */2 cadence produced.
//
// ref: references/binance/urls.md §「24hr Ticker」 / 「Kline / Candlestick」
package collector

import (
	"context"
	"encoding/json"
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
)

const (
	uptrendRedisKey = "admin:market:uptrend:v1"
	// TTL 12min vs cron 5min: gives 1 missed scan worth of headroom before the
	// frontend sees an empty cache (handleUptrend returns [] on cache miss).
	uptrendRedisTTL = 12 * time.Minute
)

type UptrendCollectorConfig struct {
	TopN         int
	Concurrency  int
	KlinesLimit  int // 1h klines per symbol; 60 covers EMA50 + warmup
	BTCSymbol    string
	FetchTimeout time.Duration // per-symbol kline fetch budget
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
		// 100 gives EMA50 + 50 warmup iters AFTER dropping the incomplete bar
		// (=99 closed). Still weight=1 per request (limit ≤ 100).
		cfg.KlinesLimit = 100
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

	btcClose, btcPct4h, err := c.fetchBTC4hPct(ctx)
	if err != nil {
		return fmt.Errorf("btc 4h ref: %w", err)
	}
	c.log.Debug().Float64("btc_close", btcClose).Float64("btc_4h_pct", btcPct4h).
		Int("candidates", len(tickers)).Msg("uptrend: starting scan")

	items := c.scanConcurrent(ctx, tickers, btcPct4h)
	sort.Slice(items, func(i, j int) bool { return items[i].RelStrength > items[j].RelStrength })

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

// USDT-margined perps only (drops SYMBOLUSDC / SYMBOLBUSD / SYMBOL_240329 etc).
// Binance perp symbols end in USDT; quarterlies have '_' before the expiry date.
func filterUSDTPerps(in []binance.Ticker24hData) []binance.Ticker24hData {
	out := make([]binance.Ticker24hData, 0, len(in))
	for _, t := range in {
		if strings.HasSuffix(t.Symbol, "USDT") && !strings.Contains(t.Symbol, "_") {
			out = append(out, t)
		}
	}
	return out
}

func (c *UptrendCollector) fetchBTC4hPct(ctx context.Context) (latestClose, pct4h float64, err error) {
	bctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	bars, err := c.fetchKlines(bctx, c.cfg.BTCSymbol)
	if err != nil {
		return 0, 0, err
	}
	bars = dropIncompleteBar(bars)
	closes := closesAsFloats(bars)
	if len(closes) < 5 {
		return 0, 0, fmt.Errorf("btc bars=%d, need ≥5", len(closes))
	}
	pct4h, err = market.PctChange(closes, 4)
	if err != nil {
		return 0, 0, err
	}
	return closes[len(closes)-1], pct4h, nil
}

func (c *UptrendCollector) scanConcurrent(ctx context.Context, ts []binance.Ticker24hData, btcPct4h float64) []market.UptrendItem {
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
			bars, err := c.fetchKlines(bctx, sym)
			if err != nil {
				c.log.Debug().Str("symbol", sym).Err(err).Msg("uptrend: klines fetch failed (skip)")
				return nil
			}
			bars = dropIncompleteBar(bars)
			closes, highs, lows, vols := ohlcvAsFloats(bars)
			item, err := market.CheckUptrend(sym, closes, highs, lows, vols, btcPct4h)
			if err != nil {
				return nil
			}
			mu.Lock()
			items = append(items, item)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return items
}

func (c *UptrendCollector) fetchKlines(ctx context.Context, symbol string) ([]binance.KlineBar, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {"1h"},
		"limit":    {strconv.Itoa(c.cfg.KlinesLimit)},
	}
	body, err := c.client.DoRead(ctx, "/fapi/v1/klines", params, 1)
	if err != nil {
		return nil, err
	}
	return binance.ParseKlines(body)
}

// dropIncompleteBar removes the last (in-progress) 1h kline returned by
// /fapi/v1/klines. Binance includes the currently-open candle in its
// response — its close evolves and volume is partial, which destabilizes
// any indicator that reads the latest bar. Stripping it makes signals
// stable for the full duration of an hour.
//
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Kline-Candlestick-Data
// "The kline currently in progress is included in the returned data set."
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
		volumes[i], _ = b.Volume.Float64()
	}
	return
}
