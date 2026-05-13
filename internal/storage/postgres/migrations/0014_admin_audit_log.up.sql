-- Phase 5.2 Round 1: generalized admin audit log.
--
-- Coexists with circuit_breaker_events (migration 0013, Round R.2). Round R.2
-- data preserved; new write operations record here. Audit view UNIONs both.
--
-- previous_state / new_state are JSONB so any write endpoint can record any
-- shape without per-action columns.
CREATE TABLE admin_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    operator       TEXT        NOT NULL,    -- 'mu' for B1 single-admin
    action_type    TEXT        NOT NULL,    -- 'halt_reset' | 'manual_close' | 'threshold_update' | ...
    resource_type  TEXT,                    -- 'trade' | 'circuit_breaker' | 'config' | 'watchlist'
    resource_id    TEXT,                    -- trade_id / config_key / symbol — TEXT for flexibility
    previous_state JSONB,
    new_state      JSONB,
    note           TEXT,
    ip_address     INET,
    user_agent     TEXT
);
CREATE INDEX admin_audit_log_ts_desc_idx     ON admin_audit_log (ts DESC);
CREATE INDEX admin_audit_log_operator_idx    ON admin_audit_log (operator, ts DESC);
CREATE INDEX admin_audit_log_action_idx      ON admin_audit_log (action_type, ts DESC);
CREATE INDEX admin_audit_log_resource_idx    ON admin_audit_log (resource_type, resource_id);
