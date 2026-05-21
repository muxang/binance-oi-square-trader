-- Reverse: shrink back to NUMERIC(10, 6) — may fail if any existing row's
-- market_cap_ratio_pct ≥ 10000. Use with care.
ALTER TABLE large_holder_ratios
    ALTER COLUMN market_cap_ratio_pct TYPE NUMERIC(10, 6);
