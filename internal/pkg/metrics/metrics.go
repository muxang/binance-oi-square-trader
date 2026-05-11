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

	// DecisionEvaluationsTotal counts Phase 3 decision_engine per-signal
	// evaluations by outcome. ~14 bounded outcome label values (see
	// internal/decision/engine.go OutcomeXxx + filters/sizing reasons).
	// Cardinality: ~14 series. No symbol label per 1.8 纪律.
	DecisionEvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_decision_evaluations_total",
			Help: "Total Phase 3 decision evaluations by outcome (trade_entering/rejected_*/sizing_*/internal_error/no_entered_signals).",
		},
		[]string{"outcome"},
	)

	// DecisionSizingDeviationPct measures step-round偏差 (TargetNotional -
	// ActualNotional) / TargetNotional × 100%. Only emitted on successful
	// sizing (Outcome=trade_entering); reject paths skip. symbol_class is a
	// 3-bucket enum (high_price/mid/low) keeping cardinality at 3 series.
	// v0.2 forward calibrates buckets after real-data P50/P95.
	DecisionSizingDeviationPct = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "trader_decision_sizing_deviation_pct",
			Help:    "Phase 3 sizing step-round deviation as percent (TargetNotional - ActualNotional)/TargetNotional × 100.",
			Buckets: []float64{0, 0.1, 0.5, 1, 2, 5, 10, 20},
		},
		[]string{"symbol_class"},
	)

	// OrdersTotal counts Phase 4 entry orders by symbol+side+decision+result.
	// Cardinality: bounded by active trading symbols × 2 sides × 2 decisions × 2 results.
	OrdersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orders_total",
			Help: "Total Phase 4 entry orders placed, by symbol+side+decision+result.",
		},
		[]string{"symbol", "side", "decision", "result"},
	)

	// DisasterStopsPlacedTotal counts Algo Service disaster stop placements.
	// Labels: symbol, result (success/failed).
	DisasterStopsPlacedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_disaster_stops_placed_total",
			Help: "Total disaster stop orders placed via Algo Service, by symbol+result.",
		},
		[]string{"symbol", "result"},
	)

	// OrderLatencySeconds histograms wall-clock time per Phase 4 entry step.
	// Labels: step — "margin" | "leverage" | "place" | "fill" | "algo".
	OrderLatencySeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "trader_order_latency_seconds",
			Help:    "Latency per Phase 4 entry order step (margin/leverage/place/fill/algo).",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30},
		},
		[]string{"step"},
	)

	// Phase 4 Round 2: retry + idempotency + halt auto-reset metrics.

	// OrdersRetryTotal counts retry attempts per signed write path.
	// Labels: path, error_code (-1021 / -2022 / network / 5xx code / etc), retry_n (1/2/3).
	OrdersRetryTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orders_retry_total",
			Help: "Phase 4 Round 2 signed write retry attempts by path+error_code+retry_n.",
		},
		[]string{"path", "error_code", "retry_n"},
	)

	// OrdersIdempotentHitTotal counts -2022 duplicate clientOrderId resolutions.
	// Each hit is a recovery from a prior partial-success state.
	OrdersIdempotentHitTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orders_idempotent_hit_total",
			Help: "Phase 4 Round 2 -2022 duplicate clientOrderId lookups.",
		},
		[]string{"symbol"},
	)

	// HaltAutoResetTotal counts circuit breaker auto-resets after halt_until expiry.
	// Labels: halt_type (disaster_stop_failed / btc_5m_crash / etc).
	HaltAutoResetTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_halt_auto_reset_total",
			Help: "Circuit breaker auto-resets after halt_until expired.",
		},
		[]string{"halt_type"},
	)

	// Phase 4 Round 3: position sync + MARGIN_CALL metrics.

	// PositionSyncRunsTotal counts 1min position_manager ticks by outcome.
	// Labels: result — "ok" | "drift" | "error" | "empty".
	PositionSyncRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_position_sync_runs_total",
			Help: "Phase 4 Round 3 position_manager tick outcomes.",
		},
		[]string{"result"},
	)

	// PositionSyncDriftTotal counts DB-vs-Binance position drift events.
	// Labels: symbol, drift_type — "qty" | "direction" | "missing".
	PositionSyncDriftTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_position_sync_drift_total",
			Help: "DB vs Binance position drift detections per tick (Round 3 logs, Round 4 halts).",
		},
		[]string{"symbol", "drift_type"},
	)

	// PositionMarginRatio is the last-tick margin_ratio = (-unrealized_pnl) / margin.
	// Gauge so we keep the latest value per symbol; > 0.8 triggers margin_call.
	PositionMarginRatio = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "trader_position_margin_ratio",
			Help: "Last-tick margin ratio (-unrealized_pnl / margin) per open position.",
		},
		[]string{"symbol"},
	)

	// MarginCallTriggeredTotal counts emergency MARKET SELL triggers from margin_ratio > 0.8.
	MarginCallTriggeredTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_margin_call_triggered_total",
			Help: "Phase 4 Round 3 emergency exits triggered by margin_ratio > 0.8.",
		},
		[]string{"symbol"},
	)
)

func init() {
	prometheus.MustRegister(
		CollectorRunsTotal, CollectorDurationSeconds,
		SignalEvaluationsTotal,
		DecisionEvaluationsTotal, DecisionSizingDeviationPct,
		OrdersTotal, DisasterStopsPlacedTotal, OrderLatencySeconds,
		OrdersRetryTotal, OrdersIdempotentHitTotal, HaltAutoResetTotal,
		PositionSyncRunsTotal, PositionSyncDriftTotal, PositionMarginRatio, MarginCallTriggeredTotal,
	)
}
