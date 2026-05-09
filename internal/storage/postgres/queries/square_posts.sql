-- name: BatchInsertSquarePosts :batchexec
-- T2 writes ~50-100 posts per 1h tick. ON CONFLICT DO NOTHING — Square posts
-- are write-once snapshots; live counters (view/like) are tracked separately
-- via T3's square_hashtag_history (per ARCHITECTURE §7).
INSERT INTO square_posts (
  id, content_text, author_id, author_name, author_type, title,
  view_count, like_count, comment_count, raw_json, fetched_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING;

-- name: GetRecentSquarePosts :many
-- T4 watchlist refresh (Phase 1.6) reads recent posts to score Square-source
-- candidates. Not used by T2 itself.
SELECT * FROM square_posts
WHERE fetched_at > $1
ORDER BY fetched_at DESC;
