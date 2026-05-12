DROP INDEX IF EXISTS idx_trades_data_source;
ALTER TABLE trade_exits DROP COLUMN IF EXISTS data_source;
ALTER TABLE trades      DROP COLUMN IF EXISTS data_source;
