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
}

// NewCirculatingSupplyCollector wires pgx-backed reader/writer.
func NewCirculatingSupplyCollector(cg *coingecko.Client, pool *pgxpool.Pool, log zerolog.Logger) *CirculatingSupplyCollector {
	q := gen.New(pool)
	return &CirculatingSupplyCollector{
		cg:        cg,
		log:       log,
		listMapFn: q.ListCoingeckoMappings,
		oiUSDFn:   q.GetLatestOIValueUSD,
		updateFn:  q.UpdateLatestMarketCapForSymbol,
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

	updated, skipped := 0, 0
	for _, m := range mappings {
		market, ok := markets[m.CoingeckoID]
		if !ok {
			skipped++
			continue
		}
		oiUSD, err := c.oiUSDFn(ctx, m.BinanceSymbol)
		if err != nil {
			// No oi_history row yet (new watchlist symbol) — skip silently.
			skipped++
			continue
		}
		if market.MarketCap <= 0 || oiUSD.IsZero() {
			skipped++
			continue
		}
		marketCap := decimal.NewFromFloat(market.MarketCap)
		supply := decimal.NewFromFloat(market.CirculatingSupply)
		ratio := oiUSD.Div(marketCap).Mul(decimal.NewFromInt(100))
		err = c.updateFn(ctx, gen.UpdateLatestMarketCapForSymbolParams{
			Symbol:            m.BinanceSymbol,
			OpenInterestUsd:   decimal.NullDecimal{Decimal: oiUSD, Valid: true},
			CirculatingSupply: decimal.NullDecimal{Decimal: supply, Valid: true},
			MarketCapRatioPct: decimal.NullDecimal{Decimal: ratio, Valid: true},
		})
		if err != nil {
			c.log.Warn().Err(err).Str("symbol", m.BinanceSymbol).Msg("circulating_supply: update failed")
			skipped++
			continue
		}
		updated++
	}
	c.log.Info().Int("mappings", len(mappings)).Int("markets", len(markets)).
		Int("updated", updated).Int("skipped", skipped).Msg("circulating_supply tick complete")
	return nil
}
