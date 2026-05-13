// Phase 5.2 Round 2.x: hand-edited queries for admin_overrides + watchlist_overrides.
// (Round 1 added migrations 0014/0015/0016; queries are added here as the tables
// have no SQL companion files yet.)
package gen

import (
	"context"
)

// WatchlistOverride mirrors one watchlist_overrides row.
type WatchlistOverride struct {
	Symbol string
	Action string // 'include' | 'exclude'
	Reason string
}

const listWatchlistOverrides = `-- name: ListWatchlistOverrides :many
SELECT symbol, action, COALESCE(reason, '')
FROM watchlist_overrides
`

// ListWatchlistOverrides returns all symbol overrides set by mu via admin Web UI.
// WatchlistCollector applies them after sortAndTruncate (exclude removes from pool;
// include force-adds even if filtered out).
func (q *Queries) ListWatchlistOverrides(ctx context.Context) ([]WatchlistOverride, error) {
	rows, err := q.db.Query(ctx, listWatchlistOverrides)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchlistOverride
	for rows.Next() {
		var o WatchlistOverride
		if err := rows.Scan(&o.Symbol, &o.Action, &o.Reason); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
