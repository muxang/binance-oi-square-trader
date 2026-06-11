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

-- name: InsertEnteringTradeWithClientID :one
-- Phase 4 entry: INSERT with client_order_id set at creation for idempotency.
-- client_order_id = t{signal_id}_r{retry_count}. Round 1 always r0.
-- Returns id so executor can reference the trade for subsequent Steps.
INSERT INTO trades (
    signal_id, symbol, direction, margin, notional, leverage, status, client_order_id
) VALUES (
    $1, $2, $3, $4, $5, $6, 'entering', $7
)
RETURNING id;

-- name: UpdateTradeClientOrderID :exec
-- Phase 4 Round 2: update client_order_id when retrying with incremented retry_count.
UPDATE trades SET client_order_id = $2 WHERE id = $1;

-- name: UpdateTradeOpen :exec
-- Phase 4: mark trade open after market order fills. entry_price = fill avgPrice.
UPDATE trades
SET status = 'open',
    entry_ts = $2,
    entry_price = $3
WHERE id = $1;

-- name: UpdateTradeFailed :exec
-- Phase 4: mark trade failed; called on order failure, fill timeout, disaster stop fail.
UPDATE trades
SET status = 'failed',
    exit_reason = $2,
    exit_ts = $3
WHERE id = $1;

-- name: UpdateTradeDisasterStop :exec
-- Phase 4: record Algo Service disaster stop order ID after successful placement.
UPDATE trades
SET binance_disaster_stop_order_id = $2
WHERE id = $1;

-- name: UpdateTradeInitialDisasterLevels :exec
-- Round R.9 data integrity: persist the actual stop_loss price + ATR used at
-- entry so post-hoc backtests can simulate alternate stop widths. Called
-- non-fatally after placeDisasterStop succeeds. atr=0 means ATR was unavailable
-- and fallback DisasterStopPct was used.
UPDATE trades
SET initial_stop_loss = $2,
    initial_atr = $3
WHERE id = $1;

-- name: UpdateTradeInitialTPLevels :exec
-- Round R.9 data integrity: persist TP1/TP2 prices used at entry for backtest
-- accuracy. Either side may be NULL if its TP placement failed. Called after
-- placeTakeProfits regardless of which TP succeeded.
UPDATE trades
SET initial_take_profit_1 = $2,
    initial_take_profit_2 = $3
WHERE id = $1;

-- name: InsertPositionState :exec
-- Phase 4: initial position state after entry fill + disaster stop placed.
-- entry_oi left NULL for v0.1 (no real-time OI at entry).
INSERT INTO position_states (
    trade_id, current_qty, highest_price, last_check_ts
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (trade_id) DO NOTHING;

-- name: InsertTradeExit :exec
-- Phase 4: record each exit event (emergency_close / tp / trailing / timeout).
-- ON CONFLICT: idempotent guard against ReconcileTick + TryReconcile race.
INSERT INTO trade_exits (trade_id, ts, type, qty, price, pnl)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (trade_id, type) DO NOTHING;

-- name: GetEnteringTradesForRecovery :many
-- Phase 4 Round 2 startup recovery: trades stuck in 'entering' with a
-- client_order_id (Round 1+ inserts). Caller queries Binance by clientOrderId
-- to reconcile actual state (FILLED → open, NEW → cancel+fail, etc).
-- Excludes Phase 3 PARTIAL legacy (client_order_id IS NULL — handled by
-- CleanupOrphanEnteringTrades).
SELECT id, signal_id, symbol, direction, margin, notional, leverage, client_order_id, retry_count
FROM trades
WHERE status = 'entering'
  AND client_order_id IS NOT NULL
ORDER BY id;

-- name: BumpTradeRetryCount :exec
-- Phase 4 Round 2: bump retry_count + update client_order_id when -2022 hit
-- doesn't resolve to existing fill (e.g. ambiguous state). r{n+1} format.
UPDATE trades
SET retry_count = retry_count + 1,
    client_order_id = $2
WHERE id = $1;

-- name: SumOpenUnrealizedSnapshot :one
-- Phase 4 Round 6 TripTotalFloatLoss: aggregate unrealized PnL across all
-- open positions using position_states.current_qty × last_known mark price.
-- Returns NULL when no open trades (caller treats as 0).
-- NOTE: uses entry_price as fallback mark (Phase 4 v0.1) — Round 6 trip
-- function computes accurate float by combining with Redis latest_price.
-- This query just returns the raw qty × entry that the trip function uses
-- to weight Redis mark prices. Trip function does the actual mark - entry math.
SELECT t.id, t.symbol, t.entry_price, ps.current_qty
FROM trades t
INNER JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status IN ('open', 'partial');

-- name: ListOpenTradesForExit :many
-- Phase 4 Round 5 + Round 3 + Round 2.x Part 3: rows exit_manager iterates each
-- 1min tick. Round 2.x Part 3 adds exit_reason — when admin Web UI manually
-- closes a trade, it pre-sets status='closing' + exit_reason='manual_close';
-- exit_manager detects the pre-set reason and runs the close pipeline immediately
-- (skipping the timeout check).
SELECT t.id, t.signal_id, t.symbol, t.direction, t.entry_ts, t.entry_price,
       t.margin, t.notional, t.leverage,
       t.binance_disaster_stop_order_id,
       t.initial_oi,
       t.exit_reason,
       ps.current_qty
FROM trades t
LEFT JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status IN ('open', 'partial', 'closing')
ORDER BY t.entry_ts ASC;

-- name: RequestManualClose :exec
-- Phase 5.2 Round 2.x Part 3: admin Web UI pre-sets the close intent.
-- exit_manager 1min cron picks up + runs the close pipeline.
-- Idempotent: only fires when trade is currently open AND no exit_reason set.
UPDATE trades
SET status = 'closing',
    exit_reason = 'manual_close'
WHERE id = $1
  AND status IN ('open', 'partial')
  AND exit_reason IS NULL;

-- name: UpdateInitialOI :exec
-- v0.2 Round 3 Module C: snapshot OI at entry time so SIGFAIL detector can
-- compute drop_pct = (initial - current) / initial. Caller passes NULL when
-- OI fetch failed at entry — detector treats NULL as "skip OI condition".
UPDATE trades SET initial_oi = $2 WHERE id = $1;

-- name: GetLatestOI :one
-- v0.2 Round 3 Module C: latest OI sample for a symbol (oi_history is hypertable;
-- index symbol_ts_desc makes this O(1)). Returns the raw `oi` column (contract
-- count) — the SIGFAIL drop semantic is "positions closing", not USD value.
SELECT oi FROM oi_history WHERE symbol = $1 ORDER BY ts DESC LIMIT 1;

-- name: UpdateTradeClosed :exec
-- Phase 4 Round 5: terminal write after SELL fill confirmed.
UPDATE trades
SET status = 'closed',
    exit_ts = $2,
    exit_price = $3,
    exit_reason = $4,
    realized_pnl = $5,
    fees = $6
WHERE id = $1;

-- name: UpdateTradeClosing :exec
-- Phase 4 Round 5: intermediate state while close is in flight. Distinguishes
-- "trader intends to close" from "open / new entry". If next tick still sees
-- 'closing', exit_manager retries cancel_algo + SELL from scratch (clientOrderId
-- carries idempotency).
UPDATE trades
SET status = 'closing'
WHERE id = $1
  AND status IN ('open', 'partial');

-- name: DeletePositionState :exec
-- Phase 4 Round 5: remove position state after trade closed (no CASCADE in init).
DELETE FROM position_states WHERE trade_id = $1;

-- name: CleanupOrphanPositionStates :execrows
-- v0.2 Catch 5 (gauge audit follow-up): startup janitor to remove position_states
-- rows whose owning trade has reached a terminal state. These accumulate when
-- pre-v0.2 close paths (markCloseFailed / emergencyExit) updated trades.status
-- but didn't DELETE the state row. Run once at startup (idempotent: re-running
-- after a clean DB returns 0 rows).
DELETE FROM position_states
WHERE trade_id IN (
  SELECT id FROM trades WHERE status IN ('closed', 'failed')
);

-- name: UpdatePositionStateSync :exec
-- Phase 4 Round 3: 1min cron sync of position_states from /fapi/v3/positionRisk.
-- highest_price = GREATEST(existing, fresh_mark) — monotonic high watermark
-- used by trailing stop logic (Round 5+). current_qty = exchange truth.
UPDATE position_states
SET current_qty = $2,
    highest_price = GREATEST(COALESCE(highest_price, $3), $3),
    last_check_ts = $4
WHERE trade_id = $1;

-- name: ListOpenTradesForSync :many
-- Phase 4 Round 3: rows position_manager iterates each tick. Returns enough
-- for drift detection (qty/direction) + Redis zset rebuild + MARGIN_CALL calc.
-- v0.2 Step 5: binance_disaster_stop_order_id added so the orphan branch can
-- defensively call algo_reconciler.TryReconcile before tripping halt.
-- Round R.4 (F1): binance_trail_algo_id added so the orphan check covers the
-- trail-fired case too. Trail S1-S4 fires far more often than disaster_stop;
-- without this column position_manager only consults disaster_stop status,
-- misses FINISHED trail closes, and trips false local_only_orphan halts
-- (mu 真盘 INJ #66 / TURBOUSDT #67 / ESPORTSUSDT #59 all hit this).
-- Round R.5 (Bug B + C): trail_stage + tp1/tp2 algo IDs so position_manager
-- can (B) check TP FINISHED before tripping drift halt and (C) pass correct
-- trail_sN exit_reason instead of hardcoded "disaster" to TryReconcile.
SELECT t.id, t.signal_id, t.symbol, t.direction, t.entry_ts, t.entry_price,
       t.margin, t.notional, t.leverage,
       t.binance_disaster_stop_order_id,
       t.binance_trail_algo_id,
       t.trail_stage,
       t.binance_tp1_algo_id,
       t.binance_tp2_algo_id,
       ps.current_qty, ps.highest_price
FROM trades t
LEFT JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status IN ('open', 'partial')
ORDER BY t.entry_ts ASC;

-- name: ListOpenTradesWithAlgo :many
-- v0.2 Gap 1 + Round 1.x + Round 2: rows algo_reconciler iterates each 1min tick.
-- Returns ALL 4 algo IDs so the reconciler can poll each independently:
--   disaster_stop / trail / tp1 / tp2.
-- R.26: + initial_take_profit_{1,2} so the -2013 ("Order does not exist") fallback
-- can use the configured TP stop price as the synthetic fill price when
-- reconciling from positionRisk diff.
-- Filter: status='open' AND has at least one algo armed.
SELECT t.id, t.symbol, t.entry_ts, t.entry_price, t.margin, t.leverage,
       t.binance_disaster_stop_order_id,
       t.binance_trail_algo_id,
       t.trail_stage,
       t.binance_tp1_algo_id,
       t.binance_tp2_algo_id,
       t.initial_take_profit_1,
       t.initial_take_profit_2,
       ps.current_qty
FROM trades t
LEFT JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status = 'open'
  AND (
    (t.binance_disaster_stop_order_id IS NOT NULL AND t.binance_disaster_stop_order_id != '')
    OR (t.binance_trail_algo_id IS NOT NULL AND t.binance_trail_algo_id != '')
    OR (t.binance_tp1_algo_id IS NOT NULL AND t.binance_tp1_algo_id != '')
    OR (t.binance_tp2_algo_id IS NOT NULL AND t.binance_tp2_algo_id != '')
  )
ORDER BY t.entry_ts ASC;

-- name: UpdateTradeTPAlgos :exec
-- v0.2 Round 2 Module A: persist the two TAKE_PROFIT_MARKET algo IDs after entry.
-- Either may be NULL when placement failed (degraded entry — trail/disaster still cover).
UPDATE trades
SET binance_tp1_algo_id = $2,
    binance_tp2_algo_id = $3
WHERE id = $1;

-- name: ClearTPAlgoID :exec
-- v0.2 Round 2 Module A: after TP1 or TP2 fires (FINISHED), null out the matching
-- algo id so next algo_polling tick stops querying it. $2 = 'tp1' or 'tp2'.
UPDATE trades
SET binance_tp1_algo_id = CASE WHEN $2 = 'tp1' THEN NULL ELSE binance_tp1_algo_id END,
    binance_tp2_algo_id = CASE WHEN $2 = 'tp2' THEN NULL ELSE binance_tp2_algo_id END
WHERE id = $1;

-- name: ClearTrailAlgoID :exec
-- R.26: invoked when algo_polling sees -2013 for the trail algo. We don't
-- reconstruct an exit row (trail's actual fire price is unknown since the
-- stop ratchets), just null the column so polling stops; orphan_sync (R.8)
-- handles the position state.
UPDATE trades
SET binance_trail_algo_id = NULL
WHERE id = $1;

-- name: ClearDisasterStopOrderID :exec
-- R.26: same as ClearTrailAlgoID but for the disaster stop. orphan_sync covers
-- the position reconcile downstream.
UPDATE trades
SET binance_disaster_stop_order_id = NULL
WHERE id = $1;

-- name: DecrementPositionQty :exec
-- v0.2 Round 2 Module A: TP partial close — reduce position_states.current_qty
-- by the partial fill amount. Round 1+ trail_upgrader reads ps.current_qty each
-- tick so it'll naturally use the reduced qty on next upgrade/ratchet.
UPDATE position_states
SET current_qty   = GREATEST(current_qty - $2, 0),
    last_check_ts = $3
WHERE trade_id = $1;

-- name: CleanupOrphanEnteringTrades :execrows
-- Phase 4 Round 1 follow-up: orphan cleanup on trader startup.
-- 命中条件: status='entering' AND entry_ts IS NULL AND client_order_id IS NULL.
-- 这只匹配 Phase 3 v0.1 PARTIAL 时期写入但未执行的遗留行 (Round 1 INSERT
-- 即设 client_order_id, Round 1+ in-flight 永远不会被误杀).
-- 不带时间阈值: 若仍存在此类行, 全部是 Phase 3 legacy, 直接 fail.
UPDATE trades
SET status = 'failed',
    exit_reason = 'orphan_cleanup_startup',
    exit_ts = NOW()
WHERE status = 'entering'
  AND entry_ts IS NULL
  AND client_order_id IS NULL;

-- name: ListOpenTradesForTrail :many
-- v0.2 Round 1 Module B: rows trail_upgrader iterates each 5min tick.
-- Includes trail_stage=0 trades so the upgrader can activate S1 at +3%.
-- LEFT JOIN position_states so current_qty (sized order) is in one query.
SELECT t.id, t.symbol, t.entry_price, t.leverage,
       t.trail_stage, t.binance_trail_algo_id, t.trail_high_price, t.trail_activation_price,
       ps.current_qty, ps.highest_price
FROM trades t
LEFT JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status = 'open'
ORDER BY t.entry_ts ASC;

-- name: UpdateTradeTrailActivate :exec
-- v0.2 Round 1 Module B: S0 → S1 activation. Sets trail_stage=1, the new
-- Binance TRAILING_STOP_MARKET algo id, and seeds trail_activation_price +
-- trail_high_price (both = current_price at activation).
UPDATE trades
SET trail_stage = 1,
    binance_trail_algo_id  = $2,
    trail_activation_price = $3,
    trail_high_price       = $3
WHERE id = $1;

-- name: UpdateTradeTrailStage :exec
-- v0.2 Round 1 Module B: stage upgrade (S1→S2, S2→S3, S3→S4) OR trader-
-- managed re-arm (S3/S4 stop ratchet up). Caller passes the new algo id
-- (or empty when S3→S4 happens without re-place).
UPDATE trades
SET trail_stage           = $2,
    binance_trail_algo_id = $3
WHERE id = $1;

-- name: UpdateTradeTrailHigh :exec
-- v0.2 Round 1 Module B: monotonic high watermark used by S3/S4 stop derivation.
-- Caller passes max(current, existing) so the write is unconditional.
UPDATE trades
SET trail_high_price = $2
WHERE id = $1;

-- name: HasActivePositionForSymbol :one
-- Round R.6 (2026-05-14, mu 真实诉求): 同 symbol 入场过滤改用持仓状态判定
-- (vs Phase 3 v0.1 的 24h 时间窗口). 真实诉求 "24 小时内只要没有仓位的代币
-- 应该可以再次开仓" — closed/failed 立即可再 entry.
--
-- 状态映射:
--   entering / open / partial / closing → reject (持仓中/in-flight)
--   closed / failed                      → allow (无活动仓位)
--
-- 顺手修原 SQL 漏掉 closing 的潜在 bug — pre-R.6 手工平仓中可叠仓.
-- $1 = symbol. 不再需要 cutoff_ts 参数.
SELECT EXISTS(
  SELECT 1 FROM trades t
  WHERE t.symbol = $1
    AND t.status IN ('entering', 'open', 'partial', 'closing')
);

