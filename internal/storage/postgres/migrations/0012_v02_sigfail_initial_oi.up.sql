-- v0.2 Round 3 Module C SIGFAIL: snapshot the symbol's open-interest (contract
-- count, oi column) at entry time so the detector can compare current vs entry
-- without re-fetching history each tick. NULL = legacy trade (pre-0012) — the
-- detector treats it as "skip OI condition for this trade".
ALTER TABLE trades ADD COLUMN initial_oi NUMERIC(36, 18);
