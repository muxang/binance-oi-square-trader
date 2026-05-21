-- name: UpsertCoingeckoMarketCache :exec
-- Round R.12.A: full-market cache for circulating_supply + market_cap, updated
-- by circulating_supply collector every 6h. UPSERT so concurrent or retried
-- writes for the same symbol overwrite cleanly.
INSERT INTO coingecko_market_cache (
    binance_symbol, coingecko_id, circulating_supply,
    market_cap_usd, current_price_usd, fetched_at
) VALUES ($1, $2, $3, $4, $5, NOW())
ON CONFLICT (binance_symbol) DO UPDATE SET
    coingecko_id      = EXCLUDED.coingecko_id,
    circulating_supply= EXCLUDED.circulating_supply,
    market_cap_usd    = EXCLUDED.market_cap_usd,
    current_price_usd = EXCLUDED.current_price_usd,
    fetched_at        = NOW();

-- name: CountCoingeckoMarketCache :one
-- Used by trader startup to decide whether to skip first-run wait — empty cache
-- triggers the same one-shot kick as the symbol_map collector pattern.
SELECT COUNT(*) FROM coingecko_market_cache;
