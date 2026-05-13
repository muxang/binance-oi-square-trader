-- v0.2 Round 1 Module B TRAILING 4-stage + Module A TP placeholders.
--
-- trail_stage:
--   0 = inactive (pre-activation; trail_upgrader will activate at +3%)
--   1 = Stage 1 (Binance native TRAILING_STOP_MARKET, callbackRate 3%)
--   2 = Stage 2 (Binance native, callbackRate 5% — Binance upper bound)
--   3 = Stage 3 (trader self-managed STOP_MARKET, callbackRate 10%)
--   4 = Stage 4 (trader self-managed STOP_MARKET, callbackRate 15%)
--
-- binance_trail_algo_id: Algo Service algoId for the current trailing/stop
--   order. S1/S2: TRAILING_STOP_MARKET. S3/S4: STOP_MARKET (trader re-places
--   as trail_high_price ratchets up). NULL = no trail algo armed.
--
-- trail_high_price: monotonic max of mark price seen since trail activation.
--   Used by S3/S4 (trader-managed) to derive stop_trigger = high × (1 - cb).
--
-- trail_activation_price: price at which S1 was first armed (entry × 1.03).
--   Audit / debug only; not used by upgrade logic.
--
-- binance_tp1/tp2_algo_id: Reserved for Module A (Round 2). Added together
--   so we don't need migration 0010b later.
ALTER TABLE trades ADD COLUMN trail_stage            SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE trades ADD COLUMN binance_trail_algo_id  TEXT;
ALTER TABLE trades ADD COLUMN trail_high_price       NUMERIC(36, 18);
ALTER TABLE trades ADD COLUMN trail_activation_price NUMERIC(36, 18);
ALTER TABLE trades ADD COLUMN binance_tp1_algo_id    TEXT;
ALTER TABLE trades ADD COLUMN binance_tp2_algo_id    TEXT;

-- Partial index on open trades with active trail (S1+). Speeds up the 5min
-- trail_upgrader sweep (typically <5 rows; full table seq scan would also work
-- but the index keeps the plan stable as the trades table grows).
CREATE INDEX trades_trail_active_idx ON trades (id) WHERE status = 'open' AND trail_stage > 0;
