-- 0009: unique constraint on (trade_id, type) for trade_exits.
-- Prevents duplicate exit rows when algo_reconciler.ReconcileTick and
-- TryReconcile race to close the same trade simultaneously.
-- Before adding the constraint, delete any existing duplicates (keep the first
-- row by id for each (trade_id, type) pair).
DELETE FROM trade_exits
WHERE id NOT IN (
    SELECT MIN(id)
    FROM trade_exits
    GROUP BY trade_id, type
);

ALTER TABLE trade_exits
    ADD CONSTRAINT uq_trade_exits_trade_type UNIQUE (trade_id, type);
