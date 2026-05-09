-- name: InsertWatchlistSnapshot :exec
-- T4 writes 1 snapshot per 1h tick. Per ARCH §7 schema the table has only
-- (ts, symbols JSONB) — per-symbol sources/score live inside the JSONB
-- payload, not as separate columns.
INSERT INTO watchlist_snapshots (ts, symbols)
VALUES ($1, $2);
