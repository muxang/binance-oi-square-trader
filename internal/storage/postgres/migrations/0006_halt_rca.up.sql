-- Phase 4 Round 4: halt root-cause-analysis table.
-- Each halt event (drift / orphan / mu-manual) writes a row with context.
-- v0.1: mu 通过 psql 或 ./trader rca-list / rca-ack 查看 + 标记 acknowledged.
-- v0.2 / Phase 5: 飞书 push RCA 链接, mu react 标记.
CREATE TABLE IF NOT EXISTS halt_rca (
    id                  BIGSERIAL PRIMARY KEY,
    halt_type           TEXT NOT NULL,
    triggered_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    context_json        JSONB NOT NULL,
    mu_acknowledged     BOOLEAN NOT NULL DEFAULT FALSE,
    mu_action           TEXT,
    mu_acknowledged_at  TIMESTAMPTZ,
    resolved_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_halt_rca_unack ON halt_rca(triggered_at DESC) WHERE NOT mu_acknowledged;
