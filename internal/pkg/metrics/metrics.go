// Package metrics defines Prometheus metric vars + init registration for
// the trader process.
//
// Framework-level metrics (collector runs / duration) live here; per-package
// metrics (e.g. binance_proxy_*) are registered inside their owning package
// (see internal/binance/proxy.go) — keeps registration close to the code
// that updates the metric.
//
// Naming convention: `trader_*` prefix for project-wide framework metrics;
// `binance_*` for direct binance API/proxy mirrors per ARCH §11.5.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// CollectorRunsTotal counts each collector run by outcome.
	// Labels:
	//   collector — collector Name() (e.g. "oi", "klines", "watchlist")
	//   outcome   — "success" | "error" | "panic"
	// Cardinality: 7 collectors × 3 outcomes = 21 series.
	CollectorRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_collector_runs_total",
			Help: "Total runs of each collector by outcome (success/error/panic).",
		},
		[]string{"collector", "outcome"},
	)

	// CollectorDurationSeconds histograms wall-clock time spent in each
	// collector's Run. Default buckets [.005, .01, .025, ..., 10] cover the
	// expected range (T6 ~200ms, T1 ~3s, T7 worst case ~7s).
	CollectorDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "trader_collector_duration_seconds",
			Help:    "Time spent in each collector run, by collector.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"collector"},
	)

	// SignalEvaluationsTotal counts Phase 2 signal_engine per-symbol
	// evaluations by outcome. {outcome} only — symbol label intentionally
	// excluded to keep cardinality bounded (530 × 4 = 2120 series risk
	// per 1.8 metrics 纪律). Per-symbol detail goes to trader.log not metrics.
	// Labels: outcome — "entered_full" | "entered_half" | "rejected" | "error".
	// Cardinality: 4 series.
	SignalEvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_signal_evaluations_total",
			Help: "Total Phase 2 signal evaluations by outcome (entered_full/entered_half/rejected/error).",
		},
		[]string{"outcome"},
	)
)

func init() {
	prometheus.MustRegister(CollectorRunsTotal, CollectorDurationSeconds, SignalEvaluationsTotal)
}
