-- Phase 4 Round 2 Step 3 (mu 决策): track last disaster_stop_failed timestamp
-- for 7-day rolling counter reset. If a new failure occurs > 7 days after
-- the previous one, counter resets to 1 (backoff starts at 1h, not whatever
-- escalated value lingered). Prevents months-old incidents from forcing 24h
-- halt on a fresh issue.
ALTER TABLE circuit_breaker_state
    ADD COLUMN IF NOT EXISTS last_disaster_stop_failed_ts TIMESTAMPTZ NULL;
