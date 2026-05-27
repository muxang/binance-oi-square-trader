-- Round R.14 (mu 2026-05-27 request): price marks / alerts. mu marks a target
-- price for a symbol; the「价格标记」collector (*/1) watches mark price and on a
-- hit flips status→triggered, sends a 🟡 warning Feishu, and the admin UI shows
-- a prominent banner until mu acknowledges.
--
-- direction is fixed at creation (frontend compares target vs the Market row's
-- current price): 'above' triggers on markPrice >= target, 'below' on <= target.
-- One-shot: a triggered mark stops being polled until mu re-arms (re-creates).
--
-- Not a hypertable — this is mutable operator state (handful of rows), not a
-- time series. Money column uses NUMERIC(36,18) per CLAUDE.md §19.
CREATE TABLE IF NOT EXISTS price_marks (
    id               BIGSERIAL PRIMARY KEY,
    symbol           TEXT             NOT NULL,
    target_price     NUMERIC(36, 18)  NOT NULL,
    direction        TEXT             NOT NULL CHECK (direction IN ('above', 'below')),
    note             TEXT             NOT NULL DEFAULT '',
    status           TEXT             NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'triggered')),
    acknowledged     BOOLEAN          NOT NULL DEFAULT FALSE,
    created_price    NUMERIC(36, 18),
    triggered_at     TIMESTAMPTZ,
    triggered_price  NUMERIC(36, 18),
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

-- Collector polls active marks; partial index keeps that scan tiny regardless
-- of how many triggered/acknowledged rows accumulate.
CREATE INDEX IF NOT EXISTS idx_price_marks_active
    ON price_marks (symbol) WHERE status = 'active';
