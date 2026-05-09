-- name: GetOIChangeTop :many
-- T4 source B: top symbols by 5min OI percentage change. Pairs each
-- symbol's latest OI sample with the prior (5min earlier) and ranks by
-- % change. The 15min window guarantees ≥2 samples per symbol given
-- T1's 5min cron.
WITH ranked AS (
    SELECT symbol, oi,
           ROW_NUMBER() OVER (PARTITION BY symbol ORDER BY ts DESC) AS rn
    FROM oi_history
    WHERE ts > NOW() - INTERVAL '15 minutes'
),
paired AS (
    SELECT a.symbol, a.oi AS now_oi, b.oi AS prev_oi
    FROM ranked a
    JOIN ranked b ON a.symbol = b.symbol AND a.rn = 1 AND b.rn = 2
)
SELECT symbol, ((now_oi - prev_oi) / prev_oi)::NUMERIC AS pct_change
FROM paired
WHERE prev_oi > 0
ORDER BY pct_change DESC NULLS LAST
LIMIT $1;
