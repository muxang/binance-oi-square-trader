package collector

import "context"

// Collector is the contract every Phase 1 periodic data-collection task
// implements. Single Run(ctx) entrypoint — no Initialize / Cleanup hooks.
// Implementations take their dependencies (DB pool, binance client, log) via
// constructors; the runner only sees a Name and a Run.
//
// Run MUST respect ctx cancellation — Runner enforces a per-tick deadline so
// a stuck collector cannot block the next cron tick. Run errors are swallowed
// at Runner level: the next tick is the natural retry, the metric records the
// failure for Grafana alerting.
type Collector interface {
	// Name identifies the collector in logs and metrics. Lowercase short
	// snake_case (e.g. "oi_history", "square_feed").
	Name() string

	// Run executes one collection tick. Must complete or return ctx.Err()
	// within the runner's per-tick deadline (default 4 minutes).
	Run(ctx context.Context) error
}
