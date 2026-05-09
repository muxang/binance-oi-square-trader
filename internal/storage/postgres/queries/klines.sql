-- name: BatchUpsertKlines :batchexec
-- T7 writes ~529 symbols × 30 bars = ~15870 rows per 5-min tick. The batch
-- variant lets sqlc generate a pgx.Batch wrapper that pings the server in a
-- single round-trip per chunk. ON CONFLICT DO UPDATE refreshes the
-- in-progress bar (last of `limit=30` rolls forward each tick).
INSERT INTO klines (symbol, timeframe, open_time, open, high, low, close, volume, quote_volume)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (symbol, timeframe, open_time) DO UPDATE
  SET open = EXCLUDED.open,
      high = EXCLUDED.high,
      low  = EXCLUDED.low,
      close = EXCLUDED.close,
      volume = EXCLUDED.volume,
      quote_volume = EXCLUDED.quote_volume;

-- name: GetLatestKlines :many
-- Phase 2 signal-engine reads the latest N 15m bars to evaluate
-- "3 consecutive closes < EMA20". Not used by the T7 collector itself.
SELECT * FROM klines
WHERE symbol = $1 AND timeframe = $2
ORDER BY open_time DESC
LIMIT $3;
