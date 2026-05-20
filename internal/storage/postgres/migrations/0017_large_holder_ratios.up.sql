-- Round R.11.A1: large holder long/short ratio + market cap ratio capture.
-- 5min cron writes account_ratio + position_ratio per (symbol, ts).
-- market_cap_ratio is best-effort from CoinGecko circulating_supply (6h refresh);
-- NULL when supply lookup fails (CoinGecko outage / symbol not on CoinGecko).
-- ref: references/binance/urls.md §「Top Trader Long Short {Position,Account} Ratio」
-- ref: references/external/coingecko.md §「/coins/markets」
-- ref: references/user-snippets/contract-monitor.js (checkLargeHolderRatio + calculateMarketCapRatio)
CREATE TABLE IF NOT EXISTS large_holder_ratios (
    symbol                    TEXT             NOT NULL,
    ts                        TIMESTAMPTZ      NOT NULL,
    account_long_short_ratio  NUMERIC(36, 18),
    position_long_short_ratio NUMERIC(36, 18),
    open_interest_usd         NUMERIC(36, 18),
    circulating_supply        NUMERIC(36, 18),
    market_cap_ratio_pct      NUMERIC(10, 6),
    PRIMARY KEY (symbol, ts)
);

CREATE INDEX IF NOT EXISTS idx_large_holder_ratios_symbol_ts_desc
    ON large_holder_ratios (symbol, ts DESC);

-- Hypertable: 5min × 283 symbols ≈ 81k rows/day. Hourly chunk size matches
-- the dashboard / signal lookup pattern (recent N rows per symbol).
SELECT create_hypertable('large_holder_ratios', 'ts',
    chunk_time_interval => INTERVAL '1 hour',
    if_not_exists => TRUE);

-- 14-day retention: ratio data is only meaningful for short-window trend
-- analysis; older rows just bloat the DB.
SELECT add_retention_policy('large_holder_ratios', INTERVAL '14 days',
    if_not_exists => TRUE);

-- CoinGecko symbol → coin_id mapping cache. Populated once at startup by
-- circulating_supply collector via /coins/list; rarely changes so 1-day TTL.
CREATE TABLE IF NOT EXISTS coingecko_symbol_map (
    binance_symbol  TEXT PRIMARY KEY,
    coingecko_id    TEXT NOT NULL,
    last_refreshed  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
