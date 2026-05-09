-- name: BatchInsertSquareMentions :batchexec
-- T2 writes one row per (post_id, symbol) cashtag mention. Weight is a
-- placeholder 1.0 in v0.1; future versions can score by recency / engagement.
-- ON CONFLICT DO NOTHING — collector pre-dedups within a post, so collisions
-- here only happen across re-fetches of the same post (handled by posts
-- DO NOTHING upstream too).
INSERT INTO square_mentions (post_id, symbol, weight, ts)
VALUES ($1, $2, $3, $4)
ON CONFLICT (post_id, symbol) DO NOTHING;
