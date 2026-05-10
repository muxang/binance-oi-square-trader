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

-- name: HasRecent24hAttemptForSymbol :one
-- Phase 3 v0.1 24h 不二次入场过滤 — 用 signals.ts JOIN (trades.entry_ts
-- 在 'entering' 状态为 NULL, Phase 4 真下单后才填). signals.ts NOT NULL
-- + trades.signal_id Phase 3 永远填 → 无遗漏. Phase 4 切回
-- HasRecent24hTradeForSymbol (entry_ts 路径).
-- $1 = symbol, $2 = cutoff_ts (caller passes NOW() - INTERVAL '24h').
SELECT EXISTS(
  SELECT 1 FROM trades t
  JOIN signals s ON s.id = t.signal_id
  WHERE t.symbol = $1
    AND s.ts > $2
    AND t.status IN ('entering', 'open', 'partial', 'closed')
);

