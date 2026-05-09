-- name: GetSquareMentionsTop :many
-- T4 source A: top symbols by Square cashtag mention count since $1.
-- Caller passes (now - 1h) for the standard 1h lookback.
SELECT symbol, COUNT(*)::BIGINT AS mention_count
FROM square_mentions
WHERE ts > $1
GROUP BY symbol
ORDER BY mention_count DESC
LIMIT $2;
