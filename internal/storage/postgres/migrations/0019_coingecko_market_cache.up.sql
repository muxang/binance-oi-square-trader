-- Round R.12.A: full-market circulating_supply + market_cap cache.
-- Decouples supply/mcap data from the watchlist-only large_holder_ratios table
-- so Market 页 can show 流动市值 / 市值占比 for any oi_history symbol (~527),
-- not just the 24 watchlist symbols.
--
-- Refreshed every 6h by circulating_supply collector (after R.12.B rewrite).
-- One row per binance_symbol; ON CONFLICT UPSERT.
--
-- ref: references/external/coingecko.md
CREATE TABLE IF NOT EXISTS coingecko_market_cache (
    binance_symbol      TEXT PRIMARY KEY,
    coingecko_id        TEXT NOT NULL,
    -- NUMERIC(40, 8): up to 32 integer digits — covers token supplies
    -- exceeding 10^18 (PEPE/BabyDoge tier) without overflow.
    circulating_supply  NUMERIC(40, 8),
    market_cap_usd      NUMERIC(40, 8),
    current_price_usd   NUMERIC(40, 12),
    fetched_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_coingecko_market_cache_fetched
    ON coingecko_market_cache (fetched_at DESC);
