ALTER TABLE trades      ADD COLUMN IF NOT EXISTS data_source VARCHAR(16) DEFAULT 'mainnet';
ALTER TABLE trade_exits ADD COLUMN IF NOT EXISTS data_source VARCHAR(16) DEFAULT 'mainnet';
CREATE INDEX IF NOT EXISTS idx_trades_data_source ON trades(data_source);
