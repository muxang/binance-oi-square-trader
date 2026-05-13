-- Phase 5.2 Round 2 prep: runtime config overrides via admin Web UI.
-- Trader reads admin_overrides on each tick (1min cron + startup), values
-- here take precedence over .env. Migration created in Round 1 so Round 2
-- endpoints can land cleanly without another migration.
--
-- Schema: key/value with JSONB value for type flexibility (numeric / array / object).
-- Audit: updated_by + updated_at + previous_value JSONB for rollback.
CREATE TABLE admin_overrides (
    key            TEXT PRIMARY KEY,        -- e.g. 'DAILY_LOSS_HALT_PCT', 'OI_GROWTH_FROM_MIN_PCT'
    value          JSONB NOT NULL,          -- {"value": 0.05} or whatever shape callers expect
    previous_value JSONB,                   -- last value before this update (for one-step rollback)
    updated_by     TEXT NOT NULL,           -- 'mu'
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    note           TEXT
);
CREATE INDEX admin_overrides_updated_at_idx ON admin_overrides (updated_at DESC);
