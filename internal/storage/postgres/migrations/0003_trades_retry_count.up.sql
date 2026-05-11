-- Phase 4 Round 2: retry_count for client_order_id idempotency.
-- client_order_id = t{signal_id}_r{retry_count}; when PlaceMarketOrder gets
-- -2022 "Duplicate Order Sent", caller bumps retry_count and tries r{n+1}.
-- Default 0 (no retry yet); Round 1 trades unaffected (all r0).
ALTER TABLE trades ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
