DROP TABLE IF EXISTS circuit_breaker_events;
ALTER TABLE circuit_breaker_state DROP COLUMN IF EXISTS manual_reset_by;
ALTER TABLE circuit_breaker_state DROP COLUMN IF EXISTS manual_reset_at;
