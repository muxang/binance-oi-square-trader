-- name: InsertAPIError :exec
-- Phase 4 Round 6: every binance API failure writes a row here for TripAPIErrorRate.
-- Cardinality is bounded by error rate × retention; periodic prune in v0.2.
INSERT INTO api_errors (ts, source, endpoint, http_code, error_code, message)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CountAPIErrorsSince :one
-- Phase 4 Round 6: 1min window count for TripAPIErrorRate.
-- Hits api_errors_ts_desc_idx.
SELECT COUNT(*) FROM api_errors WHERE ts >= $1;
