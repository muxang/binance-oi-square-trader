-- name: BatchInsertSquareHashtag :batchexec
-- T3 writes 1 row per watchlist symbol per 5min tick (~150 rows). The batch
-- variant matches T1/T2/T7 — pgx.Batch in one round-trip per chunk.
-- ON CONFLICT (symbol, ts) DO NOTHING — same-tick re-runs (rare) won't dup.
INSERT INTO square_hashtag_history (symbol, ts, content_count, view_count)
VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol, ts) DO NOTHING;
