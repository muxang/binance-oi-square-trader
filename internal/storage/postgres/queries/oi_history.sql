-- name: InsertOIHistory :batchexec
-- T1 inserts ~400 symbols × 10 limit = ~4000 rows per 5-min tick. The batch
-- variant lets sqlc generate a typed wrapper that pgx sends in one round-trip.
-- ON CONFLICT DO NOTHING handles overlap from `limit=10` covering recent
-- ticks (every fetch revisits the last 50 minutes).
INSERT INTO oi_history (symbol, ts, oi, oi_value_usd)
VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol, ts) DO NOTHING;

-- name: GetLatestOIHistory :many
-- Phase 2 signal engine reads last N 5-min OI snapshots for surge eval.
-- Caller specifies limit (default 15: LookbackPeriods 10 + buffer).
SELECT * FROM oi_history
WHERE symbol = $1
ORDER BY ts DESC
LIMIT $2;
