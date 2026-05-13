DROP INDEX IF EXISTS trades_trail_active_idx;
ALTER TABLE trades DROP COLUMN IF EXISTS binance_tp2_algo_id;
ALTER TABLE trades DROP COLUMN IF EXISTS binance_tp1_algo_id;
ALTER TABLE trades DROP COLUMN IF EXISTS trail_activation_price;
ALTER TABLE trades DROP COLUMN IF EXISTS trail_high_price;
ALTER TABLE trades DROP COLUMN IF EXISTS binance_trail_algo_id;
ALTER TABLE trades DROP COLUMN IF EXISTS trail_stage;
