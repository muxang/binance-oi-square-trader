-- Phase 5.2 Round 2 prep: watchlist include/exclude overrides via admin Web UI.
-- WatchlistCollector reads this table each 1h tick and applies before writing
-- watchlist_snapshots. mu RCA use case: SAPIENUSDT -4168 → exclude.
CREATE TABLE watchlist_overrides (
    symbol         TEXT PRIMARY KEY,
    action         TEXT NOT NULL CHECK (action IN ('include', 'exclude')),
    reason         TEXT,
    updated_by     TEXT NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX watchlist_overrides_action_idx ON watchlist_overrides (action);
