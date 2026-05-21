-- Round R.11.A2c-2 follow-up: NUMERIC(10, 6) integer part (4 digits, ≤9999.999999%)
-- overflows for micro-cap tokens where OI > 100× market_cap. Witnessed on 2026-05-21
-- BJT 06:00 cron: 5 symbols (BTC + 4 micro-caps) failed with SQLSTATE 22003.
-- (BTC was due to mapping bug, but the underlying type was still too narrow for
-- the micro-cap minority — fix both at once.)
--
-- NUMERIC(20, 6) = 14 integer digits + 6 fractional, allows ratios up to
-- 99,999,999,999,999.999999 % — astronomical, can't realistically overflow.
ALTER TABLE large_holder_ratios
    ALTER COLUMN market_cap_ratio_pct TYPE NUMERIC(20, 6);
