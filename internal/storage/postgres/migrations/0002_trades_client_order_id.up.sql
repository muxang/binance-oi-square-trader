-- Phase 4 Round 1 Catch 3: add client_order_id for order idempotency.
-- Format: t{signal_id}_r{retry_count} — Round 1 always r0; Round 2 increments on retry.
-- NULL allowed: Phase 3 'entering' rows written before this migration stay NULL.
-- UNIQUE constraint: prevents duplicate order placement on crash-restart.
ALTER TABLE trades ADD COLUMN IF NOT EXISTS client_order_id TEXT;
ALTER TABLE trades ADD CONSTRAINT trades_client_order_id_unique UNIQUE (client_order_id);
