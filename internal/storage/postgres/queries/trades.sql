-- name: GetOpenTrades :many
-- T5 reads open / partial positions to fetch latest mark price per symbol.
-- Phase 4+ decision / exit logic re-uses this query as the canonical "what
-- positions need watching" view. Returns ARCH §7 columns: side is
-- `direction` (not `side`); entry_price / entry_ts are nullable.
SELECT id, symbol, direction, entry_price, entry_ts, status
FROM trades
WHERE status IN ('open', 'partial')
ORDER BY entry_ts ASC;
