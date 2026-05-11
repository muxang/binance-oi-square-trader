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
-- Phase 4 Round 2 Step 3 (mu's decision C): exponential backoff 1h / 4h / 24h.
-- Post-update counter values: 1 → 1h, 2 → 4h, 3+ → 24h (capped).
-- 7-day rolling reset: if last failure > 7d ago, counter resets to 1 (1h halt).
-- Counter resets on success via ResetDisasterStopFailCounter (in addition).
-- All references to consecutive_disaster_stop_failures in SET clause use the
-- value BEFORE this UPDATE (Postgres semantics), so we read the pre-update
-- counter to pick the new halt_until.
UPDATE circuit_breaker_state
SET consecutive_disaster_stop_failures = CASE
        WHEN last_disaster_stop_failed_ts IS NOT NULL
             AND NOW() - last_disaster_stop_failed_ts > INTERVAL '7 days'
        THEN 1
        ELSE LEAST(consecutive_disaster_stop_failures + 1, 3)
    END,
    halt_until = NOW() + (CASE
        WHEN last_disaster_stop_failed_ts IS NOT NULL
             AND NOW() - last_disaster_stop_failed_ts > INTERVAL '7 days'
        THEN INTERVAL '1 hour'
        WHEN consecutive_disaster_stop_failures = 0
        THEN INTERVAL '1 hour'
        WHEN consecutive_disaster_stop_failures = 1
        THEN INTERVAL '4 hours'
        ELSE INTERVAL '24 hours'
    END),
    last_disaster_stop_failed_ts = NOW(),
    trading_halted = TRUE,
    halt_reason = 'disaster_stop_placement_failed'
WHERE id = 1
RETURNING halt_until, consecutive_disaster_stop_failures;

-- name: TripGenericHalt :exec
-- Phase 4 Round 4: generic halt trigger for reconcile drift / orphan / unknown.
-- Caller supplies halt_reason string + halt_until timestamp. Existing
-- maintainHaltState path auto-resets when halt_until passes (Round 2 fix).
-- Idempotent: re-tripping just refreshes the halt_until window.
UPDATE circuit_breaker_state
SET trading_halted = TRUE,
    halt_reason = $1,
    halt_until = $2
WHERE id = 1;

-- name: UpdateAfterTradeClose :exec
-- Phase 4 Round 5: rolls daily_pnl + consecutive_losses after a trade closes.
-- consecutive_losses: +1 on loss (pnl < 0), reset to 0 on win/breakeven (pnl >= 0).
-- daily_pnl: accumulates today; resets when daily_pnl_date differs from today (BJT).
-- Round 6 will read these for trip evaluation.
UPDATE circuit_breaker_state
SET consecutive_losses = CASE
        WHEN $1 < 0 THEN consecutive_losses + 1
        ELSE 0
    END,
    daily_pnl = CASE
        WHEN daily_pnl_date = $2 THEN daily_pnl + $1
        ELSE $1
    END,
    daily_pnl_date = $2
WHERE id = 1;

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
