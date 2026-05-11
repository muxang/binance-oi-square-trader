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

-- name: TripDisasterStopFailHalt :one
-- Phase 4 Round 2: exponential backoff replacing manual reset.
-- Backoff = 1h × 2^current_counter, capped 24h. Counter increments per call.
-- Counter resets on successful Algo place (ResetDisasterStopFailCounter).
-- Pre-update counter references give: 0→1h, 1→2h, 2→4h, 3→8h, 4→16h, 5+→24h cap.
-- Returns new halt_until + counter so caller can log + emit metrics.
UPDATE circuit_breaker_state
SET trading_halted = TRUE,
    halt_reason = 'disaster_stop_placement_failed',
    halt_until = NOW() + INTERVAL '1 hour' * LEAST(POWER(2, consecutive_disaster_stop_failures)::INT, 24),
    consecutive_disaster_stop_failures = consecutive_disaster_stop_failures + 1
WHERE id = 1
RETURNING halt_until, consecutive_disaster_stop_failures;

-- name: ResetDisasterStopFailCounter :exec
-- Phase 4 Round 2: called when PlaceAlgoConditionalStop succeeds.
-- Clears the consecutive failure counter so the next failure starts at 1h backoff.
UPDATE circuit_breaker_state
SET consecutive_disaster_stop_failures = 0
WHERE id = 1
  AND consecutive_disaster_stop_failures > 0;

-- name: ResetHalt :exec
-- Phase 3 auto-reset when halt_until has passed. Idempotent — safe to run
-- every tick whether or not currently halted (NULL halt_until preserved).
UPDATE circuit_breaker_state
SET trading_halted = FALSE,
    halt_reason = NULL,
    halt_until = NULL
WHERE id = 1;
