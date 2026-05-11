-- Phase 4 Round 2 Step 3: disaster_stop_failed exponential backoff counter.
-- Round 1 trip set halt_until=NULL (permanent, mu 手工 reset). Round 2 uses
-- exponential backoff: 1h, 2h, 4h, 8h, 16h, capped 24h. Counter resets on
-- successful PlaceAlgoConditionalStop (业务恢复 = 真重启).
ALTER TABLE circuit_breaker_state
    ADD COLUMN IF NOT EXISTS consecutive_disaster_stop_failures INT NOT NULL DEFAULT 0;
