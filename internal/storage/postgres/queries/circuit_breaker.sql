-- name: GetCircuitBreakerState :one
-- Phase 3 decision engine reads the single-row state to gate new entries.
-- Caller compares trading_halted + halt_until vs NOW() to decide gate / reset.
SELECT id, trading_halted, halt_reason, halt_until,
       daily_pnl, daily_pnl_date, consecutive_losses, last_btc_crash_ts
FROM circuit_breaker_state
WHERE id = 1;

-- name: TripBTCHalt :exec
-- Phase 3 decision engine triggers when BTC 5min drop > 3% (SPEC §风控熔断
-- L278). 30min halt window per SPEC. last_btc_crash_ts records the trip ts
-- so /status TG command can show recency. Other trips (daily_pnl, consecutive
-- losses, etc.) are Phase 3 v0.2 / Phase 4 — not in this query.
UPDATE circuit_breaker_state
SET trading_halted = TRUE,
    halt_reason = 'btc_5m_crash',
    halt_until = $1,
    last_btc_crash_ts = $2
WHERE id = 1;

-- name: ResetHalt :exec
-- Phase 3 auto-reset when halt_until has passed. Idempotent — safe to run
-- every tick whether or not currently halted (NULL halt_until preserved).
UPDATE circuit_breaker_state
SET trading_halted = FALSE,
    halt_reason = NULL,
    halt_until = NULL
WHERE id = 1;
