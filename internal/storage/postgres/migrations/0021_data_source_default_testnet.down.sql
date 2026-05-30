-- Rollback DEFAULT only — backfilled data NOT reverted (down migrations
-- should not destroy data).
ALTER TABLE trades      ALTER COLUMN data_source SET DEFAULT 'mainnet';
ALTER TABLE trade_exits ALTER COLUMN data_source SET DEFAULT 'mainnet';
