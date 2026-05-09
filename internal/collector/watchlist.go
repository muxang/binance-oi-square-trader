// T4 collector — watchlist refresh. Every 1h gathers candidates from 4
// SPEC §监控池 sources (Square cashtag / OI top / price top / open
// positions), filters by listing-age + 24h quoteVolume + blacklist,
// sorts (score DESC, qv DESC, symbol ASC) + truncates to MaxSize.
// Persists snapshot row + writes Redis. On any failure preserves old
// Redis (T3 / Phase 2 keep using stale pool — Redis outage must NOT
// silently feed wrong symbols).

package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
	"trader/internal/storage/postgres/gen"
)

// WatchlistQueries is the minimum DB surface T4 needs (CLAUDE.md §18 —
// accept interfaces in the consumer). 3 reader methods feed source A/B/C;
// InsertWatchlistSnapshot persists the chosen pool. *gen.Queries satisfies
// this implicitly.
type WatchlistQueries interface {
	GetSquareMentionsTop(ctx context.Context, arg gen.GetSquareMentionsTopParams) ([]gen.GetSquareMentionsTopRow, error)
	GetOIChangeTop(ctx context.Context, limit int32) ([]gen.GetOIChangeTopRow, error)
	GetKlinesPriceChangeTop(ctx context.Context, limit int32) ([]gen.GetKlinesPriceChangeTopRow, error)
	InsertWatchlistSnapshot(ctx context.Context, arg gen.InsertWatchlistSnapshotParams) error
}

// WatchlistSymbolMeta exposes only GetOnboardDates from *binance.SymbolService
// (T4 doesn't care about IsValidPerpetual / ListSymbols here).
type WatchlistSymbolMeta interface {
	GetOnboardDates(ctx context.Context) (map[string]int64, error)
}

// WatchlistTickerFetcher exposes only FetchAll24hTicker from *binance.Client.
type WatchlistTickerFetcher interface {
	FetchAll24hTicker(ctx context.Context) ([]binance.Ticker24hData, error)
}

type WatchlistCollectorConfig struct {
	SquareTopN            int
	OITopN                int
	PriceTopN             int
	MaxSize               int
	MinSize               int
	MinListingDays        int
	MinQuoteVolume        decimal.Decimal
	Blacklist             []string
	LeverageTokenSuffixes []string
	RedisKey              string
}

type symbolEntry struct {
	Symbol      string          `json:"symbol"`
	Sources     []string        `json:"sources"`
	Score       int             `json:"score"`
	QuoteVolume decimal.Decimal `json:"-"` // sort key only, not persisted
}

type WatchlistCollector struct {
	symbolService WatchlistSymbolMeta
	ticker        WatchlistTickerFetcher
	queries       WatchlistQueries
	redis         *redis.Client
	log           zerolog.Logger
	cfg           WatchlistCollectorConfig
	nowFunc       func() time.Time
}

func NewWatchlistCollector(symbolService WatchlistSymbolMeta, ticker WatchlistTickerFetcher, queries WatchlistQueries, rdb *redis.Client, log zerolog.Logger, cfg WatchlistCollectorConfig) *WatchlistCollector {
	cfg = watchlistDefaults(cfg)
	return &WatchlistCollector{
		symbolService: symbolService,
		ticker:        ticker,
		queries:       queries,
		redis:         rdb,
		log:           log,
		cfg:           cfg,
		nowFunc:       timez.NowUTC,
	}
}

// watchlistDefaults uses single-line ifs (vs multi-line in other collectors)
// to keep this file under budget after the writers split — 8 fields × 3
// multi-line ifs would cost 16 extra lines.
func watchlistDefaults(cfg WatchlistCollectorConfig) WatchlistCollectorConfig {
	if cfg.SquareTopN == 0 {
		cfg.SquareTopN = 50
	}
	if cfg.OITopN == 0 {
		cfg.OITopN = 30
	}
	if cfg.PriceTopN == 0 {
		cfg.PriceTopN = 20
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = 150
	}
	if cfg.MinSize == 0 {
		cfg.MinSize = 50
	}
	if cfg.MinListingDays == 0 {
		cfg.MinListingDays = 7
	}
	if cfg.MinQuoteVolume.IsZero() {
		cfg.MinQuoteVolume = decimal.NewFromInt(10000000)
	}
	if cfg.RedisKey == "" {
		cfg.RedisKey = "watchlist:current"
	}
	return cfg
}

func (c *WatchlistCollector) Name() string { return "watchlist" }

func (c *WatchlistCollector) Run(ctx context.Context) error {
	now := c.nowFunc()

	sources, err := c.gatherSources(ctx, now)
	if err != nil {
		return fmt.Errorf("gather sources: %w", err)
	}
	candidates := c.mergeAndScore(sources)
	if len(candidates) == 0 {
		c.log.Error().Msg("watchlist: no candidates from any source — preserving old Redis")
		return nil
	}

	tickerMap, err := c.fetchTickerMap(ctx)
	if err != nil {
		return fmt.Errorf("ticker24h: %w", err)
	}
	onboardMap, err := c.symbolService.GetOnboardDates(ctx)
	if err != nil {
		return fmt.Errorf("onboard dates: %w", err)
	}

	filtered := c.applyFilters(candidates, tickerMap, onboardMap, now)
	if len(filtered) == 0 {
		c.log.Error().Int("candidates", len(candidates)).Msg("watchlist: all candidates filtered out — preserving old Redis")
		return nil
	}
	pool := c.sortAndTruncate(filtered)

	if err := c.insertSnapshot(ctx, now, pool); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := c.updateRedis(ctx, pool); err != nil {
		c.log.Error().Err(err).Msg("watchlist: redis SET failed (snapshot persisted, Redis missed)")
	}

	c.log.Info().
		Int("pool_size", len(pool)).
		Int("candidates", len(candidates)).
		Int("filtered_out", len(candidates)-len(filtered)).
		Msg("watchlist tick complete")
	if len(pool) < c.cfg.MinSize {
		c.log.Warn().Int("size", len(pool)).Int("min", c.cfg.MinSize).Msg("watchlist below min size")
	}
	return nil
}

// gatherSources fans out the 4 source SQL queries via errgroup. Any source
// SQL failing aborts the tick — partial inputs would skew score weighting.
// (Different from fetchSingleHashtag's "errgroup never propagates" pattern:
// hashtag retry treats per-symbol failure as independent; T4 sources are
// joint inputs to a single weighting decision.)
func (c *WatchlistCollector) gatherSources(ctx context.Context, now time.Time) (map[string][]string, error) {
	var squareSyms, oiSyms, priceSyms []string
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		rows, err := c.queries.GetSquareMentionsTop(gctx, gen.GetSquareMentionsTopParams{
			Ts: now.Add(-1 * time.Hour), Limit: int32(c.cfg.SquareTopN),
		})
		if err != nil {
			return fmt.Errorf("square: %w", err)
		}
		squareSyms = make([]string, len(rows))
		for i, r := range rows {
			squareSyms[i] = r.Symbol
		}
		return nil
	})
	g.Go(func() error {
		rows, err := c.queries.GetOIChangeTop(gctx, int32(c.cfg.OITopN))
		if err != nil {
			return fmt.Errorf("oi: %w", err)
		}
		oiSyms = make([]string, len(rows))
		for i, r := range rows {
			oiSyms[i] = r.Symbol
		}
		return nil
	})
	g.Go(func() error {
		rows, err := c.queries.GetKlinesPriceChangeTop(gctx, int32(c.cfg.PriceTopN))
		if err != nil {
			return fmt.Errorf("price: %w", err)
		}
		priceSyms = make([]string, len(rows))
		for i, r := range rows {
			priceSyms[i] = r.Symbol
		}
		return nil
	})
	// D source (positions) — Phase 4+ only; Phase 1 leaves nil.

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return map[string][]string{
		"square": squareSyms, "oi": oiSyms, "price": priceSyms, "position": nil,
	}, nil
}

// mergeAndScore: same symbol across N sources → score=N, sources=[...].
func (c *WatchlistCollector) mergeAndScore(sources map[string][]string) []symbolEntry {
	m := make(map[string]*symbolEntry)
	for source, symbols := range sources {
		for _, sym := range symbols {
			if e, ok := m[sym]; ok {
				e.Sources = append(e.Sources, source)
				e.Score++
			} else {
				m[sym] = &symbolEntry{Symbol: sym, Sources: []string{source}, Score: 1}
			}
		}
	}
	out := make([]symbolEntry, 0, len(m))
	for _, e := range m {
		out = append(out, *e)
	}
	return out
}

func (c *WatchlistCollector) fetchTickerMap(ctx context.Context) (map[string]decimal.Decimal, error) {
	data, err := c.ticker.FetchAll24hTicker(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]decimal.Decimal, len(data))
	for _, d := range data {
		out[d.Symbol] = d.QuoteVolume
	}
	return out, nil
}

// applyFilters drops candidates by 4 SPEC §监控池 rules. Order matters
// only for short-circuit efficiency (cheap checks first).
func (c *WatchlistCollector) applyFilters(candidates []symbolEntry, tickerMap map[string]decimal.Decimal, onboardMap map[string]int64, now time.Time) []symbolEntry {
	blacklist := make(map[string]struct{}, len(c.cfg.Blacklist))
	for _, b := range c.cfg.Blacklist {
		blacklist[b] = struct{}{}
	}
	nowMs := now.UnixMilli()
	minAgeMs := int64(c.cfg.MinListingDays) * 86400000
	out := make([]symbolEntry, 0, len(candidates))
	for _, e := range candidates {
		base := strings.TrimSuffix(e.Symbol, "USDT")
		if _, bad := blacklist[base]; bad {
			continue
		}
		isLeverageTok := false
		for _, suffix := range c.cfg.LeverageTokenSuffixes {
			if strings.HasSuffix(e.Symbol, suffix) {
				isLeverageTok = true
				break
			}
		}
		if isLeverageTok {
			continue
		}
		onboardMs, ok := onboardMap[e.Symbol]
		if !ok {
			continue
		}
		if nowMs-onboardMs < minAgeMs {
			continue
		}
		qv, ok := tickerMap[e.Symbol]
		if !ok {
			continue
		}
		if qv.LessThan(c.cfg.MinQuoteVolume) {
			continue
		}
		e.QuoteVolume = qv
		out = append(out, e)
	}
	return out
}

// sortAndTruncate: score DESC, qv DESC, symbol ASC; cap at MaxSize.
func (c *WatchlistCollector) sortAndTruncate(filtered []symbolEntry) []symbolEntry {
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if !a.QuoteVolume.Equal(b.QuoteVolume) {
			return a.QuoteVolume.GreaterThan(b.QuoteVolume)
		}
		return a.Symbol < b.Symbol
	})
	if len(filtered) > c.cfg.MaxSize {
		filtered = filtered[:c.cfg.MaxSize]
	}
	return filtered
}
