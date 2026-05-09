package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"trader/internal/storage/postgres/gen"
)

// insertSnapshot persists the rich JSONB form of the pool — each entry has
// {symbol, sources, score} so Phase 2 signal-engine can read source/score
// without re-deriving from raw history. Per ARCH §7 schema (ts, symbols).
func (c *WatchlistCollector) insertSnapshot(ctx context.Context, ts time.Time, pool []symbolEntry) error {
	payload, err := json.Marshal(pool)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return c.queries.InsertWatchlistSnapshot(ctx, gen.InsertWatchlistSnapshotParams{Ts: ts, Symbols: payload})
}

// updateRedis writes the simple symbol-string array form — T3 (and Phase 2
// hot judgement) reads this format. Sources / score live in the snapshot
// JSONB only. TTL=0 (永久, per ARCH §7 Redis Key 约定).
func (c *WatchlistCollector) updateRedis(ctx context.Context, pool []symbolEntry) error {
	symbols := make([]string, len(pool))
	for i, e := range pool {
		symbols[i] = e.Symbol
	}
	payload, err := json.Marshal(symbols)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return c.redis.Set(ctx, c.cfg.RedisKey, payload, 0).Err()
}
