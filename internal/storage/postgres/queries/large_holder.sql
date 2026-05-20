-- name: InsertLargeHolderRatio :exec
-- Round R.11.A2c-1: large_holder collector writes one row per (symbol, ts).
-- Upsert keeps idempotency if the collector retries the same tick.
-- market_cap_ratio_pct / open_interest_usd / circulating_supply are filled by
-- the CoinGecko collector (R.11.A2c-2) — left NULL here.
INSERT INTO large_holder_ratios (
    symbol, ts, account_long_short_ratio, position_long_short_ratio
) VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol, ts) DO UPDATE SET
    account_long_short_ratio  = EXCLUDED.account_long_short_ratio,
    position_long_short_ratio = EXCLUDED.position_long_short_ratio;
