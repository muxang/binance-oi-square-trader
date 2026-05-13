-- v0.2 Round R.1 Part 2: audit trail for manual halt resets.
--
-- manual_reset_at / manual_reset_by: latest manual reset timestamp + actor.
-- Cleared on next auto-trip. Used by admin UI to show "last manual reset by mu at 14:32".
--
-- circuit_breaker_events: rolling audit log. Every trip + every manual reset
-- writes one row. ts_desc index supports "last 10 events" dashboard query.
ALTER TABLE circuit_breaker_state ADD COLUMN manual_reset_at TIMESTAMPTZ;
ALTER TABLE circuit_breaker_state ADD COLUMN manual_reset_by TEXT;

CREATE TABLE circuit_breaker_events (
    id                  BIGSERIAL PRIMARY KEY,
    ts                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type          TEXT NOT NULL,        -- 'auto_trip' | 'manual_reset' | 'auto_reset'
    halt_reason         TEXT,                 -- the halt reason at the event (for context)
    halt_until_before   TIMESTAMPTZ,          -- for manual_reset: when auto-reset would have fired
    actor               TEXT,                 -- 'mu' for manual_reset; 'auto' for auto_*
    daily_pnl_snapshot  NUMERIC(36, 18),      -- daily_pnl at event time (audit)
    consec_losses_snapshot SMALLINT,           -- consecutive_losses at event time (audit)
    note                TEXT
);
CREATE INDEX circuit_breaker_events_ts_desc_idx ON circuit_breaker_events (ts DESC);
