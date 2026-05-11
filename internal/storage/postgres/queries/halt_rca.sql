-- name: InsertHaltRCA :one
-- Phase 4 Round 4: write a halt root-cause record when reconcile detects
-- local_only_orphan / binance_only_unknown / drift_exceeded.
INSERT INTO halt_rca (halt_type, context_json)
VALUES ($1, $2)
RETURNING id, triggered_at;

-- name: ListUnacknowledgedHaltRCA :many
-- Phase 4 Round 4: ./trader rca-list shows un-acked halts most-recent first.
SELECT id, halt_type, triggered_at, context_json
FROM halt_rca
WHERE NOT mu_acknowledged
ORDER BY triggered_at DESC
LIMIT 50;

-- name: AcknowledgeHaltRCA :exec
-- Phase 4 Round 4: ./trader rca-ack <id> --action=resolved marks a halt
-- as reviewed. Does NOT auto-reset the circuit breaker (that's Round 2
-- backoff timer's job).
UPDATE halt_rca
SET mu_acknowledged = TRUE,
    mu_action = $2,
    mu_acknowledged_at = NOW(),
    resolved_at = CASE WHEN $2 = 'resolved' THEN NOW() ELSE NULL END
WHERE id = $1
  AND NOT mu_acknowledged;
