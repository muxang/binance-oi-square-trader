ALTER TABLE trades DROP CONSTRAINT IF EXISTS trades_client_order_id_unique;
ALTER TABLE trades DROP COLUMN IF EXISTS client_order_id;
