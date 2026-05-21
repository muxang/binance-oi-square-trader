package collector

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/coingecko"
	"trader/internal/storage/postgres/gen"
)

// coingeckoBatchSize matches /coins/markets ids= cap (CoinGecko Demo).
const coingeckoBatchSize = 250

// CirculatingSupplyCollector runs every 6 hours: for each watchlist symbol that
// has a coingecko_symbol_map row, fetch circulating_supply + market_cap from
// /coins/markets (batched ≤250 ids), compute market_cap_ratio_pct = OI_USD ÷
// market_cap × 100 using the freshest oi_history.oi_value_usd, then UPDATE
// the latest large_holder_ratios row in place.
//
// ref: references/external/coingecko.md
// ref: references/user-snippets/contract-monitor.js (calculateMarketCapRatio)
//
// Failures tolerated per-symbol — partial data is fine. Missing mapping ⇒ row
// stays with NULL market_cap_ratio_pct.
type CirculatingSupplyCollector struct {
	cg          *coingecko.Client
	log         zerolog.Logger
	listMapFn   func(ctx context.Context) ([]gen.ListCoingeckoMappingsRow, error)
	oiUSDFn     func(ctx context.Context, symbol string) (decimal.Decimal, error)
	updateFn    func(ctx context.Context, arg gen.UpdateLatestMarketCapForSymbolParams) error
	cacheFn     func(ctx context.Context, arg gen.UpsertCoingeckoMarketCacheParams) error
}

// NewCirculatingSupplyCollector wires pgx-backed reader/writer.
// R.12.B (2026-05-21): now writes coingecko_market_cache (full market) instead
// of just UPDATEing watchlist subset of large_holder_ratios. The legacy
// UpdateLatestMarketCapForSymbol path is kept for the acct/pos ratio rows
// that DO have a large_holder_ratios entry (watchlist).
func NewCirculatingSupplyCollector(cg *coingecko.Client, pool *pgxpool.Pool, log zerolog.Logger) *CirculatingSupplyCollector {
	q := gen.New(pool)
	return &CirculatingSupplyCollector{
		cg:        cg,
		log:       log,
		listMapFn: q.ListCoingeckoMappings,
		oiUSDFn:   q.GetLatestOIValueUSD,
		updateFn:  q.UpdateLatestMarketCapForSymbol,
		cacheFn:   q.UpsertCoingeckoMarketCache,
	}
}

func (c *CirculatingSupplyCollector) Name() string { return "circulating_supply" }

// Run pulls the full coingecko_symbol_map, queries /coins/markets in batches
// of ≤250, then back-fills large_holder_ratios for each symbol that has both
// (a) a CoinGecko market response and (b) an oi_history.oi_value_usd row.
func (c *CirculatingSupplyCollector) Run(ctx context.Context) error {
	mappings, err := c.listMapFn(ctx)
	if err != nil {
		return fmt.Errorf("list mappings: %w", err)
	}
	if len(mappings) == 0 {
		return errors.New("circulating_supply: empty mapping table — A2b symbol_map collector hasn't run")
	}

	// ids → binance_symbol reverse index. CoinGecko ids are unique by construction.
	idToBinSym := make(map[string]string, len(mappings))
	ids := make([]string, 0, len(mappings))
	for _, m := range mappings {
		idToBinSym[m.CoingeckoID] = m.BinanceSymbol
		ids = append(ids, m.CoingeckoID)
	}

	// markets: coingecko_id → MarketData. Batched ≤250 per HTTP call.
	markets := make(map[string]coingecko.MarketData, len(ids))
	for start := 0; start < len(ids); start += coingeckoBatchSize {
		end := start + coingeckoBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch, err := c.cg.GetMarkets(ctx, ids[start:end], "usd")
		if err != nil {
			// Tolerate batch failure — log and continue with whatever we have.
			c.log.Warn().Err(err).Int("batch_start", start).Int("batch_end", end).
				Msg("circulating_supply: /coins/markets batch failed (will proceed with partial data)")
			continue
		}
		for _, m := range batch {
			markets[m.ID] = m
		}
	}
	if len(markets) == 0 {
		return errors.New("circulating_supply: CoinGecko returned 0 markets across all batches")
	}

	cached, lhUpdated, skipped := 0, 0, 0
	for _, m := range mappings {
		market, ok := markets[m.CoingeckoID]
		if !ok {
			skipped++
			continue
		}
		// market.CirculatingSupply / market.MarketCap may be 0 — CoinGecko sometimes
		// has the symbol cataloged but no live market data. Skip rather than write
		// noise rows (downstream COALESCE would treat 0 as "no data" anyway).
		if market.MarketCap <= 0 && market.CirculatingSupply <= 0 {
			skipped++
			continue
		}

		var supply, mcap, price decimal.NullDecimal
		if market.CirculatingSupply > 0 {
			supply = decimal.NullDecimal{Decimal: decimal.NewFromFloat(market.CirculatingSupply), Valid: true}
		}
		if market.MarketCap > 0 {
			mcap = decimal.NullDecimal{Decimal: decimal.NewFromFloat(market.MarketCap), Valid: true}
		}
		if market.CurrentPrice > 0 {
			price = decimal.NullDecimal{Decimal: decimal.NewFromFloat(market.CurrentPrice), Valid: true}
		}

		// R.12.B: full-market cache write (every mapped symbol gets a row, regardless
		// of watchlist membership — this is the new Market 页 source of truth).
		if err := c.cacheFn(ctx, gen.UpsertCoingeckoMarketCacheParams{
			BinanceSymbol:     m.BinanceSymbol,
			CoingeckoID:       m.CoingeckoID,
			CirculatingSupply: supply,
			MarketCapUsd:      mcap,
			CurrentPriceUsd:   price,
		}); err != nil {
			c.log.Warn().Err(err).Str("symbol", m.BinanceSymbol).Msg("circulating_supply: cache upsert failed")
			skipped++
			continue
		}
		cached++

		// Legacy R.11.A2c-2 path: still UPDATE the watchlist row's
		// market_cap_ratio_pct so the existing acct/pos ratio sidebar charts
		// keep their inline mcap column. Non-fatal — 0 rows affected for
		// non-watchlist symbols (no large_holder_ratios row exists).
		oiUSD, err := c.oiUSDFn(ctx, m.BinanceSymbol)
		if err != nil || oiUSD.IsZero() || !mcap.Valid {
			continue
		}
		ratio := oiUSD.Div(mcap.Decimal).Mul(decimal.NewFromInt(100))
		if err := c.updateFn(ctx, gen.UpdateLatestMarketCapForSymbolParams{
			Symbol:            m.BinanceSymbol,
			OpenInterestUsd:   decimal.NullDecimal{Decimal: oiUSD, Valid: true},
			CirculatingSupply: supply,
			MarketCapRatioPct: decimal.NullDecimal{Decimal: ratio, Valid: true},
		}); err != nil {
			c.log.Debug().Err(err).Str("symbol", m.BinanceSymbol).Msg("circulating_supply: legacy lh row update skipped")
			continue
		}
		lhUpdated++
	}
	c.log.Info().Int("mappings", len(mappings)).Int("markets", len(markets)).
		Int("cached", cached).Int("lh_updated", lhUpdated).Int("skipped", skipped).
		Msg("circulating_supply tick complete")
	return nil
}
