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
// Symbol disambiguation strategy (revised 2026-05-21 after BTC → batcat bug):
//   1. Strip the "USDT" quote-asset suffix → BASE (lowercased).
//   2. First lookup the top-250 by-market-cap snapshot (/coins/markets
//      order=market_cap_desc per_page=250). Top tokens are canonical by
//      virtue of being the highest-mcap holder of that symbol — BTC wins
//      over batcat, ETH over ethereum-pow, etc. Single API call covers
//      ~all major tokens mu cares about.
//   3. If miss in top-250: fall back to /coins/list shortest-id heuristic
//      (works for genuine micro-caps where the canonical IS the shortest).
//   4. If still zero candidates: skip (NULL market_cap_ratio downstream).
//
// mu can hand-correct mapping rows via UPDATE coingecko_symbol_map ... if
// a heuristic decision is still wrong.
type CoingeckoSymbolMapCollector struct {
	cg         *coingecko.Client
	log        zerolog.Logger
	symbolsFn  func(ctx context.Context) ([]string, error)
	upsertFn   func(ctx context.Context, arg gen.UpsertCoingeckoMappingParams) error
	countFn    func(ctx context.Context) (int64, error)
}

// NewCoingeckoSymbolMapCollector wires the collector with pgx-backed reader/writer.
// R.12.B (2026-05-21): symbol source switched from watchlist (~24) to
// oi_history.GetActiveOISymbols (~527 full market) — mu wants 流通市值 for
// every USDⓈ-M perp, not just trading candidates.
func NewCoingeckoSymbolMapCollector(cg *coingecko.Client, pool *pgxpool.Pool, log zerolog.Logger) *CoingeckoSymbolMapCollector {
	q := gen.New(pool)
	return &CoingeckoSymbolMapCollector{
		cg:        cg,
		log:       log,
		symbolsFn: q.GetActiveOISymbols, // R.12.B: full market via oi_history
		upsertFn:  q.UpsertCoingeckoMapping,
		countFn:   q.CountCoingeckoMappings,
	}
}

func (c *CoingeckoSymbolMapCollector) Name() string { return "coingecko_symbol_map" }

// Run pulls top-250-by-mcap + the full /coins/list catalog, then rebuilds
// mappings for the full oi_history universe (~527 USDT-perp) with
// market_cap_desc priority (canonical) and shortest-id fallback (micro-caps).
// Tolerant of partial CoinGecko outage.
func (c *CoingeckoSymbolMapCollector) Run(ctx context.Context) error {
	symbols, err := c.symbolsFn(ctx)
	if err != nil {
		return fmt.Errorf("active oi symbols: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New("coingecko_symbol_map: no active oi symbols")
	}

	// Step 1: top-1000 by market_cap (canonical layer, 4 pages × 250).
	// Top-250 alone leaves out alt coins in the $10M-$1B mcap range — the
	// shortest-id fallback then mis-maps them (EDEN, PUMP, BB, etc).
	// 4 batch calls / 6h = trivial vs CoinGecko Demo 30/min.
	topByMcap := make(map[string]string)
	if topMarkets, err := c.cg.GetMarketsTopByMcapMulti(ctx, 1000); err == nil {
		for _, m := range topMarkets {
			s := strings.ToLower(m.Symbol)
			if _, exists := topByMcap[s]; !exists {
				topByMcap[s] = m.ID // first occurrence = highest market_cap for this symbol
			}
		}
		c.log.Info().Int("top_mcap_pool", len(topByMcap)).Msg("coingecko_symbol_map: top-mcap pool loaded")
	} else {
		c.log.Warn().Err(err).Msg("coingecko_symbol_map: top-mcap pull failed (will use shortest-id fallback only)")
	}

	catalog, err := c.cg.ListCoins(ctx)
	if err != nil {
		return fmt.Errorf("coingecko /coins/list: %w", err)
	}
	if len(catalog) == 0 {
		return errors.New("coingecko_symbol_map: catalog returned 0 coins")
	}

	// Fallback index for micro-caps not in top-250.
	bySymbol := make(map[string][]string, len(catalog))
	for _, coin := range catalog {
		s := strings.ToLower(coin.Symbol)
		bySymbol[s] = append(bySymbol[s], coin.ID)
	}

	mapped, skipped, fromTop := 0, 0, 0
	for _, binSym := range symbols {
		base := strings.ToLower(strings.TrimSuffix(binSym, "USDT"))
		if base == "" {
			skipped++
			continue
		}
		// Priority 1: top-250 by market_cap.
		id, ok := topByMcap[base]
		if ok {
			fromTop++
		} else {
			// Priority 2: shortest-id heuristic over /coins/list catalog.
			candidates := bySymbol[base]
			if len(candidates) == 0 {
				skipped++
				continue
			}
			id = pickCanonicalID(candidates)
		}
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
	c.log.Info().Int("active_symbols", len(symbols)).Int("mapped", mapped).
		Int("from_top_mcap", fromTop).Int("skipped", skipped).
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
