-- name: GetLatestWatchlistSymbols :many
-- Returns the symbol set from the most-recent watchlist_snapshots row.
-- Used by Phase 1.4 (watchlist refresh) and downstream collectors that need
-- the current monitor pool. Phase 1.1 only generates the binding to keep the
-- sqlc layer stable; the first real call site lands in 1.4.
SELECT DISTINCT (entry->>'symbol')::text AS symbol
FROM watchlist_snapshots, jsonb_array_elements(symbols) AS entry
WHERE id = (SELECT MAX(id) FROM watchlist_snapshots);
