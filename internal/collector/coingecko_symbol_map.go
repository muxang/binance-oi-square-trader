package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"trader/internal/coingecko"
	"trader/internal/storage/postgres/gen"
)

// CoingeckoSymbolMapCollector refreshes the (binance_symbol → coingecko_id)
// table twice daily (cron 0 0,12 * * * BJT). Mapping rarely changes but new
// listings happen often enough that a 12h cadence keeps the marketcap path
// useful.
//
// ref: references/external/coingecko.md
// ref: references/user-snippets/contract-monitor.js (getCirculatingSupply
//      implicit dependency — JS never exposed its mapping mechanism)
//
// Symbol disambiguation strategy:
//   1. Strip the "USDT" quote-asset suffix from each watchlist symbol → BASE.
//   2. Lowercase BASE → look up in the /coins/list catalog.
//   3. If exactly one candidate id: that's the mapping.
//   4. If multiple candidates: pick the lexicographically *shortest* id
//      (heuristic: forks/wrapped variants usually have longer ids like
//      "bitcoin-cash" / "wrapped-bitcoin" while the canonical is "bitcoin").
//      Ties broken by alphabetical order for determinism.
//   5. If zero candidates: skip (mapping row not written; circulating_supply
//      collector will write NULL market_cap_ratio for that symbol).
//
// The heuristic is not perfect — mu can manually override via direct UPDATE
// on coingecko_symbol_map.binance_symbol if a row is wrong.
type CoingeckoSymbolMapCollector struct {
	cg          *coingecko.Client
	log         zerolog.Logger
	watchlistFn func(ctx context.Context) ([]string, error)
	upsertFn    func(ctx context.Context, arg gen.UpsertCoingeckoMappingParams) error
	countFn     func(ctx context.Context) (int64, error)
}

// NewCoingeckoSymbolMapCollector wires the collector with pgx-backed reader/writer.
func NewCoingeckoSymbolMapCollector(cg *coingecko.Client, pool *pgxpool.Pool, log zerolog.Logger) *CoingeckoSymbolMapCollector {
	q := gen.New(pool)
	return &CoingeckoSymbolMapCollector{
		cg:          cg,
		log:         log,
		watchlistFn: q.GetLatestWatchlistSymbols,
		upsertFn:    q.UpsertCoingeckoMapping,
		countFn:     q.CountCoingeckoMappings,
	}
}

func (c *CoingeckoSymbolMapCollector) Name() string { return "coingecko_symbol_map" }

// Run pulls the full CoinGecko catalog (~15k coins) and rebuilds the watchlist
// mapping. Tolerates per-symbol miss (just skips that row). Returns error only
// on CoinGecko outage or empty watchlist.
func (c *CoingeckoSymbolMapCollector) Run(ctx context.Context) error {
	symbols, err := c.watchlistFn(ctx)
	if err != nil {
		return fmt.Errorf("watchlist: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New("coingecko_symbol_map: empty watchlist")
	}

	catalog, err := c.cg.ListCoins(ctx)
	if err != nil {
		return fmt.Errorf("coingecko /coins/list: %w", err)
	}
	if len(catalog) == 0 {
		return errors.New("coingecko_symbol_map: catalog returned 0 coins")
	}

	// Build symbol-lowercase → []id index (catalog is ~15k entries; one pass).
	bySymbol := make(map[string][]string, len(catalog))
	for _, coin := range catalog {
		s := strings.ToLower(coin.Symbol)
		bySymbol[s] = append(bySymbol[s], coin.ID)
	}

	mapped, skipped := 0, 0
	for _, binSym := range symbols {
		base := strings.ToLower(strings.TrimSuffix(binSym, "USDT"))
		if base == "" {
			skipped++
			continue
		}
		candidates := bySymbol[base]
		if len(candidates) == 0 {
			skipped++
			continue
		}
		id := pickCanonicalID(candidates)
		if err := c.upsertFn(ctx, gen.UpsertCoingeckoMappingParams{
			BinanceSymbol: binSym,
			CoingeckoID:   id,
		}); err != nil {
			c.log.Warn().Err(err).Str("symbol", binSym).Str("id", id).
				Msg("coingecko_symbol_map: upsert failed")
			skipped++
			continue
		}
		mapped++
	}
	c.log.Info().Int("watchlist", len(symbols)).Int("mapped", mapped).Int("skipped", skipped).
		Int("catalog", len(catalog)).Msg("coingecko_symbol_map tick complete")
	return nil
}

// pickCanonicalID picks the shortest id (ties broken alphabetically) from a
// candidate set. Forks/wrappers typically have longer slugs ("wrapped-bitcoin",
// "bitcoin-cash") while canonical is just "bitcoin". Not perfect — mu can
// manually correct mapping rows when wrong.
func pickCanonicalID(ids []string) string {
	if len(ids) == 1 {
		return ids[0]
	}
	best := ids[0]
	for _, id := range ids[1:] {
		if len(id) < len(best) || (len(id) == len(best) && id < best) {
			best = id
		}
	}
	return best
}

// EnsureMappingPopulated runs Run once if the mapping table is empty.
// Called by main.go at startup before the 12h cron registers, so the first
// circulating-supply tick has data to work with. Failures are non-fatal —
// trader keeps running; CoinGecko collector will retry on its own schedule.
func (c *CoingeckoSymbolMapCollector) EnsureMappingPopulated(ctx context.Context) {
	n, err := c.countFn(ctx)
	if err != nil {
		c.log.Warn().Err(err).Msg("coingecko_symbol_map: count check failed at startup (will retry on cron)")
		return
	}
	if n > 0 {
		c.log.Info().Int64("existing_mappings", n).Msg("coingecko_symbol_map: skip startup refresh (table populated)")
		return
	}
	c.log.Info().Msg("coingecko_symbol_map: empty table at startup → running one-shot refresh")
	if err := c.Run(ctx); err != nil {
		c.log.Warn().Err(err).Msg("coingecko_symbol_map: startup refresh failed (will retry on cron)")
	}
}
