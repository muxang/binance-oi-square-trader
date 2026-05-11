-- Phase 4 Round 6: track last losing close for consec_losses 24h window check.
-- SPEC §风控熔断 #2: 5 consecutive losses within 24h → halt.
-- Round 5 UpdateAfterTradeClose already tracks consecutive_losses counter.
-- This column lets the trip evaluator distinguish "5 losses in 24h" from
-- "5 losses spread over weeks" (latter shouldn't halt).
ALTER TABLE circuit_breaker_state
    ADD COLUMN IF NOT EXISTS last_loss_at TIMESTAMPTZ NULL;
