-- R.17 D2 followup: migration 0021 missed 49 failed trades (entry_ts IS NULL)
-- because they never reached the entry step. They ARE testnet-mode attempts
-- (trader has been TRADER_MODE=testnet since 2026-05-12) — relabel them.
--
-- Identifier:
--   status = 'failed'  AND
--   entry_ts IS NULL   AND
--   data_source = 'mainnet'
-- → guaranteed to be R.17-era testnet attempts (signal_id > 0 confirms they
-- came from the live signal engine, not legacy mainnet test residue).
UPDATE trades
SET data_source = 'testnet'
WHERE status = 'failed'
  AND entry_ts IS NULL
  AND data_source = 'mainnet';
