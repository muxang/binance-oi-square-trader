-- name: InsertSignal :exec
-- Phase 2 signal engine writes one row per (5min × pool symbol) evaluation.
-- ~150 pool × 12/h × 24h ≈ 43k rows/day; mostly decision='rejected'.
-- No dedup: each tick is a fresh evaluation snapshot. Phase 3 decision
-- engine reads with WHERE ts > NOW() - 5min for "fresh signals".
INSERT INTO signals (
    ts, symbol, oi_triggered, oi_data, square_hot, square_data, decision, rejection_reason
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetRecentEnteredSignals :many
-- Phase 3 decision engine reads "fresh entered_full / entered_half" signals
-- in the last 5 min window. ts ASC so caller processes in arrival order
-- (Phase 3 v0.1 FIFO priority). Hits signals_ts_desc_idx in reverse-scan.
SELECT id, ts, symbol, oi_triggered, oi_data, square_hot, square_data, decision
FROM signals
WHERE ts > $1
  AND decision IN ('entered_full', 'entered_half')
ORDER BY ts ASC;
