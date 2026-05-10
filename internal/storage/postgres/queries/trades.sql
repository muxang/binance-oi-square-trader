-- name: GetOpenTrades :many
-- T5 reads open / partial positions to fetch latest mark price per symbol.
-- Phase 4+ decision / exit logic re-uses this query as the canonical "what
-- positions need watching" view. Returns ARCH §7 columns: side is
-- `direction` (not `side`); entry_price / entry_ts are nullable.
SELECT id, symbol, direction, entry_price, entry_ts, status
FROM trades
WHERE status IN ('open', 'partial')
ORDER BY entry_ts ASC;

-- name: CountActiveTrades :one
-- Phase 3 decision engine 仓位上限检查 (SPEC §仓位规则 ≤ 5).
-- 'entering' (Phase 3 写入) / 'open' / 'partial' 都算 active.
-- Hits trades_status_entry_ts_desc_idx.
SELECT COUNT(*) FROM trades
WHERE status IN ('entering', 'open', 'partial');

-- name: HasRecent24hTradeForSymbol :one
-- Phase 3 决策引擎 24h 不二次入场过滤 (SPEC §仓位规则 L191 + §全局过滤 L205).
-- 任一 active OR closed 的 trade 在 24h 内开过 → skip 该 symbol.
-- $1 = symbol, $2 = cutoff_ts (NOW() - INTERVAL '24h' from caller).
-- Hits trades_symbol_status_idx (symbol prefix).
SELECT EXISTS(
  SELECT 1 FROM trades
  WHERE symbol = $1
    AND entry_ts IS NOT NULL
    AND entry_ts > $2
);

-- name: InsertEnteringTrade :one
-- Phase 3 写入决策记录, status='entering'. Phase 4 真下单后 UPDATE 填
-- entry_ts / entry_price / binance_position_id / status='open'.
-- Returns id so caller can log + future Phase 4 references.
INSERT INTO trades (
    signal_id, symbol, direction, margin, notional, leverage, status
) VALUES (
    $1, $2, $3, $4, $5, $6, 'entering'
)
RETURNING id;
