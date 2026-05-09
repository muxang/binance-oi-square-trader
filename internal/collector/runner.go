package collector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"trader/internal/pkg/timez"
)

// PerTickTimeout caps every collector invocation. 4 minutes matches Square
// hashtag tracking's hard deadline (SPEC §「Square 跟踪并发约束」, ARCHITECTURE
// §9.5); shorter collectors finish well within. A stuck collector cannot
// block the next tick.
const PerTickTimeout = 4 * time.Minute

// Metric hooks — Phase 1.0 placeholders. Phase 1.9 swaps these for real
// Prometheus counters via package-var assignment (no API change).
var (
	metricStarted = func(string) {}
	metricSuccess = func(string) {}
	metricFailed  = func(string, string) {}
)

// Runner wraps a single robfig/cron instance and dispatches collectors with
// uniform logging, recover, ctx timeout, and metric accounting.
type Runner struct {
	cron *cron.Cron
	log  zerolog.Logger
	mu   sync.Mutex
	reg  []registered
}

type registered struct {
	name     string
	schedule string
}

// New constructs a Runner using BJT as cron's location (CLAUDE.md time zone
// discipline: cron decisions in BJT — daily reset, daily report — while
// business comparisons stay UTC).
func New(log zerolog.Logger) *Runner {
	return &Runner{
		cron: cron.New(cron.WithLocation(timez.BJT)),
		log:  log,
	}
}

// Register schedules a collector. Each tick spawns a fresh goroutine; failures
// are isolated (log + metric, no fanout). schedule is a robfig/cron expression
// (5- or 6-field, or `@every 30s` style).
func (r *Runner) Register(c Collector, schedule string) error {
	name := c.Name()
	if _, err := r.cron.AddFunc(schedule, func() { r.run(c) }); err != nil {
		return fmt.Errorf("collector %q schedule %q: %w", name, schedule, err)
	}
	r.mu.Lock()
	r.reg = append(r.reg, registered{name: name, schedule: schedule})
	r.mu.Unlock()
	return nil
}

// Start spawns the cron scheduler. Returns immediately.
func (r *Runner) Start() {
	r.cron.Start()
	r.log.Info().Int("collectors", len(r.reg)).Msg("collector runner started")
}

// Stop halts the scheduler and waits for in-flight collectors up to timeout.
// A still-running tick that exceeds timeout means a collector ignored ctx —
// returns an error so the operator notices.
func (r *Runner) Stop(timeout time.Duration) error {
	stopCtx := r.cron.Stop()
	select {
	case <-stopCtx.Done():
		r.log.Info().Msg("collector runner stopped")
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("collector runner: in-flight collectors did not finish within %s", timeout)
	}
}

// run is the per-tick wrapper. Owns ctx timeout, panic recovery, metric
// emission. Errors are swallowed — the next cron tick is the retry.
func (r *Runner) run(c Collector) {
	name := c.Name()
	traceID := newTraceID()
	log := r.log.With().Str("collector", name).Str("trace_id", traceID).Logger()

	ctx, cancel := context.WithTimeout(context.Background(), PerTickTimeout)
	defer cancel()

	started := timez.NowUTC()
	metricStarted(name)
	log.Info().Time("started_at", started).Msg("collector started")

	defer func() {
		if rec := recover(); rec != nil {
			metricFailed(name, "panic")
			log.Error().Interface("panic", rec).Msg("collector panicked")
		}
	}()

	if err := c.Run(ctx); err != nil {
		metricFailed(name, "error")
		log.Error().Err(err).Dur("elapsed", timez.NowUTC().Sub(started)).Msg("collector failed")
		return
	}
	metricSuccess(name)
	log.Info().Dur("elapsed", timez.NowUTC().Sub(started)).Msg("collector completed")
}

// newTraceID returns 8 hex chars. Short enough for human reading; collisions
// across concurrent runs are statistically negligible at our scale.
func newTraceID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
