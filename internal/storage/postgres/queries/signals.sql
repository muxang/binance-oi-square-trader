-- name: InsertSignal :exec
-- Phase 2 signal engine writes one row per (5min × pool symbol) evaluation.
-- ~150 pool × 12/h × 24h ≈ 43k rows/day; mostly decision='rejected'.
-- No dedup: each tick is a fresh evaluation snapshot. Phase 3 decision
-- engine reads with WHERE ts > NOW() - 5min for "fresh signals".
INSERT INTO signals (
    ts, symbol, oi_triggered, oi_data, square_hot, square_data, decision, rejection_reason
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
