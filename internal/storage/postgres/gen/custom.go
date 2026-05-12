// Hand-written extensions to the sqlc-generated Queries type.
// Do NOT regenerate with `make sqlc` — this file will be overwritten.
// Keep changes minimal and document why sqlc cannot generate them.
package gen

import "context"

// InsertTradeExitIdempotent inserts a trade_exits row and returns the number of
// rows actually written (1 on success, 0 when skipped by ON CONFLICT DO NOTHING).
// Used by AlgoReconciler to detect duplicate close attempts from concurrent ticks.
func (q *Queries) InsertTradeExitIdempotent(ctx context.Context, arg InsertTradeExitParams) (int64, error) {
	tag, err := q.db.Exec(ctx, insertTradeExit, arg.TradeID, arg.Ts, arg.Type, arg.Qty, arg.Price, arg.Pnl)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
