-- name: GetKlinesPriceChangeTop :many
-- T4 source C: top symbols by 24h price change. For each symbol pairs
-- the newest 15m close with the oldest 15m close in the last 24h window;
-- on cold-start (< 24h of data) this naturally falls back to the oldest
-- bar available (SPEC §6 fallback semantic).
WITH ranked AS (
    SELECT symbol, close,
           ROW_NUMBER() OVER (PARTITION BY symbol ORDER BY open_time DESC) AS newest_rn,
           ROW_NUMBER() OVER (PARTITION BY symbol ORDER BY open_time ASC)  AS oldest_rn
    FROM klines
    WHERE timeframe = '15m' AND open_time > NOW() - INTERVAL '24 hours'
)
SELECT
    n.symbol,
    ((n.close - o.close) / o.close)::NUMERIC AS pct_change
FROM ranked n
JOIN ranked o ON n.symbol = o.symbol
WHERE n.newest_rn = 1 AND o.oldest_rn = 1 AND o.close > 0
ORDER BY pct_change DESC NULLS LAST
LIMIT $1;
