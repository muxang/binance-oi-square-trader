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
)

func init() {
	prometheus.MustRegister(CollectorRunsTotal, CollectorDurationSeconds)
}
