-- ============================================================================
-- 0001_init.down.sql — reverses 0001_init.up.sql.
--
-- Drop order respects FK dependencies (children before parents). DROP TABLE on
-- a hypertable cascades to its underlying chunks. The timescaledb extension
-- itself is left in place — other DBs in the same cluster may still need it,
-- and re-applying the up migration uses CREATE EXTENSION IF NOT EXISTS.
-- ============================================================================

DROP TABLE IF EXISTS api_errors;
DROP TABLE IF EXISTS trade_exits;            -- FK → trades
DROP TABLE IF EXISTS position_states;        -- FK → trades
DROP TABLE IF EXISTS trades;                 -- FK → signals
DROP TABLE IF EXISTS signals;
DROP TABLE IF EXISTS watchlist_snapshots;
DROP TABLE IF EXISTS square_mentions;        -- FK → square_posts
DROP TABLE IF EXISTS square_posts;
DROP TABLE IF EXISTS circuit_breaker_state;
DROP TABLE IF EXISTS square_hashtag_history; -- hypertable
DROP TABLE IF EXISTS klines;                 -- hypertable
DROP TABLE IF EXISTS oi_history;             -- hypertable
