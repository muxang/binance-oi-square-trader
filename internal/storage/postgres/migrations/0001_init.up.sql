-- ============================================================================
-- 0001_init.up.sql
--
-- Initial schema for binance-oi-square-trader.
-- Anchored 1:1 to ARCHITECTURE.md §7. 12 tables total:
--   3 hypertables  : oi_history, klines, square_hashtag_history
--   9 relational   : square_posts, square_mentions, watchlist_snapshots,
--                    signals, trades, trade_exits, position_states,
--                    circuit_breaker_state, api_errors
--
-- Conventions (CLAUDE.md §19):
--   - All time columns: TIMESTAMPTZ
--   - All money columns: NUMERIC(36, 18)
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ----------------------------------------------------------------------------
-- 时序表 (TimescaleDB hypertables, 1-day chunks)
-- ----------------------------------------------------------------------------

-- 1) oi_history — Open Interest snapshot for every USDⓈ-M perp symbol.
CREATE TABLE oi_history (
    symbol       TEXT NOT NULL,
    ts           TIMESTAMPTZ NOT NULL,
    oi           NUMERIC(36, 18) NOT NULL,
    oi_value_usd NUMERIC(36, 18) NOT NULL,
    PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('oi_history', 'ts', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX oi_history_symbol_ts_desc_idx ON oi_history (symbol, ts DESC);

-- 2) klines — Cached 15min K-lines for ATR / EMA computation.
CREATE TABLE klines (
    symbol       TEXT NOT NULL,
    timeframe    TEXT NOT NULL,
    open_time    TIMESTAMPTZ NOT NULL,
    open         NUMERIC(36, 18) NOT NULL,
    high         NUMERIC(36, 18) NOT NULL,
    low          NUMERIC(36, 18) NOT NULL,
    close        NUMERIC(36, 18) NOT NULL,
    volume       NUMERIC(36, 18) NOT NULL,
    quote_volume NUMERIC(36, 18) NOT NULL,
    PRIMARY KEY (symbol, timeframe, open_time)
);
SELECT create_hypertable('klines', 'open_time', chunk_time_interval => INTERVAL '1 day');

-- 3) square_hashtag_history — content_count / view_count time series per symbol.
CREATE TABLE square_hashtag_history (
    symbol        TEXT NOT NULL,
    ts            TIMESTAMPTZ NOT NULL,
    content_count BIGINT NOT NULL,
    view_count    BIGINT NOT NULL,
    PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('square_hashtag_history', 'ts', chunk_time_interval => INTERVAL '1 day');

-- ----------------------------------------------------------------------------
-- 关系表 (普通 Postgres 表)
-- ----------------------------------------------------------------------------

-- 4) square_posts — Raw Square recommendation feed posts.
CREATE TABLE square_posts (
    id            TEXT PRIMARY KEY,
    fetched_at    TIMESTAMPTZ NOT NULL,
    author_id     TEXT,
    author_type   TEXT,
    author_name   TEXT,
    title         TEXT,
    content_text  TEXT,
    view_count    BIGINT,
    like_count    BIGINT,
    comment_count BIGINT,
    raw_json      JSONB
);
CREATE INDEX square_posts_fetched_at_desc_idx ON square_posts (fetched_at DESC);

-- 5) square_mentions — Cashtag mentions extracted from Square posts.
CREATE TABLE square_mentions (
    post_id TEXT REFERENCES square_posts(id),
    symbol  TEXT NOT NULL,
    weight  NUMERIC(8, 4) NOT NULL DEFAULT 1.0,
    ts      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (post_id, symbol)
);
CREATE INDEX square_mentions_symbol_ts_desc_idx ON square_mentions (symbol, ts DESC);

-- 6) watchlist_snapshots — Hourly snapshot of the active monitor pool.
CREATE TABLE watchlist_snapshots (
    id      BIGSERIAL PRIMARY KEY,
    ts      TIMESTAMPTZ NOT NULL,
    symbols JSONB NOT NULL  -- [{symbol, sources: ['square','oi','price','position'], score}]
);

-- 7) signals — Per-evaluation OI surge + Square hot decision record.
CREATE TABLE signals (
    id               BIGSERIAL PRIMARY KEY,
    ts               TIMESTAMPTZ NOT NULL,
    symbol           TEXT NOT NULL,
    oi_triggered     BOOLEAN NOT NULL,
    oi_data          JSONB,
    square_hot       BOOLEAN NOT NULL,
    square_data      JSONB,
    decision         TEXT NOT NULL,  -- entered_full / entered_half / rejected
    rejection_reason TEXT
);
CREATE INDEX signals_ts_desc_idx        ON signals (ts DESC);
CREATE INDEX signals_symbol_ts_desc_idx ON signals (symbol, ts DESC);

-- 8) trades — Lifecycle record of an opened position.
CREATE TABLE trades (
    id                             BIGSERIAL PRIMARY KEY,
    signal_id                      BIGINT REFERENCES signals(id),
    symbol                         TEXT NOT NULL,
    direction                      TEXT NOT NULL DEFAULT 'LONG',
    entry_ts                       TIMESTAMPTZ,
    entry_price                    NUMERIC(36, 18),
    margin                         NUMERIC(36, 18) NOT NULL,
    notional                       NUMERIC(36, 18) NOT NULL,
    leverage                       SMALLINT NOT NULL DEFAULT 10,
    initial_atr                    NUMERIC(36, 18),
    initial_stop_loss              NUMERIC(36, 18),
    initial_take_profit_1          NUMERIC(36, 18),
    initial_take_profit_2          NUMERIC(36, 18),
    binance_position_id            TEXT,
    binance_disaster_stop_order_id TEXT,
    status                         TEXT NOT NULL,  -- entering / open / partial / closed / orphan
    exit_ts                        TIMESTAMPTZ,
    exit_price                     NUMERIC(36, 18),
    exit_reason                    TEXT,
    realized_pnl                   NUMERIC(36, 18),
    fees                           NUMERIC(36, 18),
    raw_events                     JSONB
);
CREATE INDEX trades_status_entry_ts_desc_idx ON trades (status, entry_ts DESC);
CREATE INDEX trades_symbol_status_idx        ON trades (symbol, status);

-- 9) trade_exits — Partial-exit / full-exit events per trade.
CREATE TABLE trade_exits (
    id       BIGSERIAL PRIMARY KEY,
    trade_id BIGINT REFERENCES trades(id),
    ts       TIMESTAMPTZ NOT NULL,
    type     TEXT NOT NULL,  -- tp_stage1 / tp_stage2 / trailing / signal_fail / disaster / soft_timeout / hard_timeout / manual / rollback
    qty      NUMERIC(36, 18) NOT NULL,
    price    NUMERIC(36, 18) NOT NULL,
    pnl      NUMERIC(36, 18) NOT NULL
);

-- 10) position_states — Live state for in-flight positions (state machine).
CREATE TABLE position_states (
    trade_id             BIGINT PRIMARY KEY REFERENCES trades(id),
    current_qty          NUMERIC(36, 18) NOT NULL,
    highest_price        NUMERIC(36, 18),
    trailing_stop_active BOOLEAN NOT NULL DEFAULT FALSE,
    trailing_stop_price  NUMERIC(36, 18),
    tp_stage1_done       BOOLEAN NOT NULL DEFAULT FALSE,
    tp_stage2_done       BOOLEAN NOT NULL DEFAULT FALSE,
    entry_oi             NUMERIC(36, 18),
    last_check_ts        TIMESTAMPTZ
);

-- 11) circuit_breaker_state — Global circuit-breaker state (single row).
CREATE TABLE circuit_breaker_state (
    id                 SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    trading_halted     BOOLEAN NOT NULL DEFAULT FALSE,
    halt_reason        TEXT,
    halt_until         TIMESTAMPTZ,
    daily_pnl          NUMERIC(36, 18) NOT NULL DEFAULT 0,
    daily_pnl_date     DATE NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')::DATE,
    consecutive_losses SMALLINT NOT NULL DEFAULT 0,
    last_btc_crash_ts  TIMESTAMPTZ
);
-- Idempotent seed: re-applying the migration after a manual restore that
-- already inserted the row must not violate CHECK (id = 1).
INSERT INTO circuit_breaker_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- 12) api_errors — API error sliding-window log for circuit breaker.
CREATE TABLE api_errors (
    id         BIGSERIAL PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL,
    source     TEXT NOT NULL,    -- binance_rest / binance_ws / square
    endpoint   TEXT,
    http_code  INT,
    error_code INT,
    message    TEXT
);
CREATE INDEX api_errors_ts_desc_idx ON api_errors (ts DESC);
