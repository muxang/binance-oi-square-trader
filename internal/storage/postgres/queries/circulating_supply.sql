-- name: GetLatestOIValueUSD :one
-- Round R.11.A2c-2: read most-recent USD-valued open interest for a symbol
-- (BAPI sumOpenInterestValue, already in USD). Used to compute market_cap_ratio
-- = OI_USD / market_cap × 100 without re-fetching from BAPI.
SELECT oi_value_usd FROM oi_history WHERE symbol = $1 ORDER BY ts DESC LIMIT 1;

-- name: UpdateLatestMarketCapForSymbol :exec
-- Round R.11.A2c-2: fill the most-recent large_holder_ratios row's market-cap
-- columns with the 6h CoinGecko snapshot. Subquery picks MAX(ts) for the symbol
-- so the update is idempotent against concurrent large_holder collector writes.
-- No-op if no large_holder_ratios row exists yet (subquery NULL → 0 rows affected).
UPDATE large_holder_ratios
SET open_interest_usd    = $2,
    circulating_supply   = $3,
    market_cap_ratio_pct = $4
WHERE symbol = $1
  AND ts = (SELECT MAX(ts) FROM large_holder_ratios WHERE symbol = $1);
