-- name: UpsertCoingeckoMapping :exec
-- Round R.11.A2b: idempotent insert/update for binance_symbol → coingecko_id.
-- Called per row by the 12h refresh collector; conflict updates the id +
-- last_refreshed timestamp (mapping may drift if CoinGecko adds disambiguators).
INSERT INTO coingecko_symbol_map (binance_symbol, coingecko_id, last_refreshed)
VALUES ($1, $2, NOW())
ON CONFLICT (binance_symbol) DO UPDATE SET
    coingecko_id   = EXCLUDED.coingecko_id,
    last_refreshed = NOW();

-- name: CountCoingeckoMappings :one
-- Used by trader startup to decide whether to force a one-shot refresh before
-- registering the 12h cron. 0 rows → first launch, refresh immediately.
SELECT COUNT(*) FROM coingecko_symbol_map;

-- name: ListCoingeckoMappings :many
-- Read all mappings (string→string). Used by circulating-supply collector to
-- translate watchlist binance symbols into CoinGecko ids before /coins/markets.
SELECT binance_symbol, coingecko_id FROM coingecko_symbol_map;
