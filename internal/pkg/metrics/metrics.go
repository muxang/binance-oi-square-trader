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

	// CollectorLastTickSeconds is the Unix timestamp (seconds) of the last
	// successful run per collector. Set on success only; never set on error/panic.
	// Used by admin-api dashboard to show "last tick X min ago" and infer stale status.
	CollectorLastTickSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "trader_collector_last_tick_seconds",
			Help: "Unix timestamp of the last successful collector run.",
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

	// Phase 4 Round 4: bidirectional reconcile + halt RCA metrics.

	// PositionDriftHaltTotal counts halts triggered by drift > 5%.
	// Labels: symbol, drift_type — "qty" | "direction".
	PositionDriftHaltTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_position_drift_halt_total",
			Help: "Phase 4 Round 4 halts triggered by reconcile drift > threshold.",
		},
		[]string{"symbol", "drift_type"},
	)

	// PositionLocalOnlyOrphanTotal counts local_only_orphan events
	// (DB has open trade, Binance has no matching position).
	PositionLocalOnlyOrphanTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "trader_position_local_only_orphan_total",
			Help: "Phase 4 Round 4 local-only orphan detections (DB has, Binance doesn't).",
		},
	)

	// PositionBinanceOnlyUnknownTotal counts binance_only_unknown events
	// (Binance has a position, DB has no corresponding open trade).
	PositionBinanceOnlyUnknownTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "trader_position_binance_only_unknown_total",
			Help: "Phase 4 Round 4 binance-only unknown detections (Binance has, DB doesn't).",
		},
	)

	// HaltRCAPendingTotal counts unacknowledged halt_rca rows by halt_type.
	// Treated as a counter (mu acks set it to 0 via the cmd-line tool).
	HaltRCAPendingTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_halt_rca_pending_total",
			Help: "Phase 4 Round 4 halt_rca records created per halt_type.",
		},
		[]string{"halt_type"},
	)

	// Phase 4 Round 5: exit + timeout + realized PnL metrics.

	// ExitsTotal counts exits by symbol+reason+result.
	// Labels: symbol, exit_reason (soft_timeout / hard_timeout / disaster / manual),
	// result (success / sell_failed / db_failed).
	ExitsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_exits_total",
			Help: "Phase 4 Round 5 trade exits by symbol+reason+result.",
		},
		[]string{"symbol", "exit_reason", "result"},
	)

	// ExitLatencySeconds histograms wall-clock per close step.
	// Labels: step — "cancel_algo" | "place_sell" | "fill" | "db".
	ExitLatencySeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "trader_exit_latency_seconds",
			Help:    "Latency per Round 5 close pipeline step.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30},
		},
		[]string{"step"},
	)

	// RealizedPnlTotal sums realized PnL by symbol+sign (positive / negative / zero).
	// Counter type means each .Add() must be non-negative — we Add |pnl| and label
	// the sign so Grafana can compute net via positive - negative.
	RealizedPnlTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_realized_pnl_total",
			Help: "Phase 4 Round 5 realized PnL totals by symbol+sign.",
		},
		[]string{"symbol", "sign"},
	)

	// PositionHoldDurationHours histograms hold time at close by exit_reason.
	PositionHoldDurationHours = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "trader_position_hold_duration_hours",
			Help:    "Hold duration at exit time by exit_reason.",
			Buckets: []float64{0.5, 1, 2, 4, 8, 12, 24, 48, 72, 120, 168},
		},
		[]string{"exit_reason"},
	)

	// Phase 4 Round 6: 5-item circuit breaker trip metrics.

	// CircuitBreakerTripsTotal counts trips by trip_type.
	CircuitBreakerTripsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_circuit_breaker_trips_total",
			Help: "Phase 4 Round 6 5-item circuit breaker trips by trip_type.",
		},
		[]string{"trip_type"},
	)

	// CircuitBreakerState is 1 when halted, 0 when normal. Set every tick.
	CircuitBreakerState = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_circuit_breaker_state",
			Help: "1=halted, 0=normal.",
		},
	)

	// AccountBalanceUSDT last-tick USDT availableBalance fetched for trip evaluation.
	AccountBalanceUSDT = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_account_balance_usdt",
			Help: "Last-tick USDT availableBalance (Round 6 evaluation).",
		},
	)

	// DailyPnlUSDT is the rolling daily PnL value tracked in circuit_breaker_state.
	DailyPnlUSDT = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_daily_pnl_usdt",
			Help: "Rolling daily realized PnL (BJT).",
		},
	)

	// ConsecutiveLossesGauge is the current consecutive_losses counter.
	ConsecutiveLossesGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_consecutive_losses",
			Help: "Current consecutive losing closes count.",
		},
	)

	// BTC30MinDropPct is the last-tick 30min BTC drop percentage (positive when down).
	BTC30MinDropPct = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_btc_30min_drop_pct",
			Help: "BTC 30min drop percentage (positive when dropping).",
		},
	)

	// UnrealizedPnlTotalUSDT is the sum of unrealized PnL across all open positions.
	UnrealizedPnlTotalUSDT = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "trader_unrealized_pnl_total_usdt",
			Help: "Sum of unrealized PnL across all open positions.",
		},
	)

	// Phase 4 Round 7: restart recovery metrics.

	// RestartRecoveryRunsTotal counts startup recovery passes by outcome.
	// Labels: result — "clean" | "halt_triggered" | "with_reconciles" | "error".
	RestartRecoveryRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_restart_recovery_runs_total",
			Help: "Phase 4 Round 7 startup recovery pass outcomes.",
		},
		[]string{"result"},
	)

	// v0.2 Gap 1: Algo polling metrics.

	// AlgoPollingRunsTotal counts polling ticks by outcome.
	// Labels: result — "ok" | "empty" | "err".
	AlgoPollingRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_algo_polling_runs_total",
			Help: "v0.2 Gap 1 Algo polling ticks (ok/empty/err).",
		},
		[]string{"result"},
	)

	// AlgoTriggeredTotal counts auto-close reconciles by symbol+exit_reason.
	// exit_reason will always be 'disaster' for Algo TRIGGERED (kept as label
	// for symmetry with ExitsTotal).
	AlgoTriggeredTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_algo_triggered_total",
			Help: "v0.2 Gap 1 Algo TRIGGERED auto-close reconciles per symbol.",
		},
		[]string{"symbol", "exit_reason"},
	)

	// TrailingStageUpgradeTotal counts trail stage transitions per direction.
	// Labels: from_stage / to_stage (e.g. "0"→"1" = S0 activation; "2"→"3" = trader-managed switch).
	// Cardinality: ≤ 4 series ("0"→"1", "1"→"2", "2"→"3", "3"→"4").
	TrailingStageUpgradeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_trailing_stage_upgrade_total",
			Help: "v0.2 Round 1 trail stage transitions (S0→S1 activation through S3→S4).",
		},
		[]string{"from_stage", "to_stage"},
	)

	// TPFilledTotal counts TAKE_PROFIT_MARKET partial fills per symbol+stage.
	// Labels: symbol, stage ("tp1" / "tp2"). Cardinality bounded by symbols × 2.
	TPFilledTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_tp_filled_total",
			Help: "v0.2 Round 2 TAKE_PROFIT_MARKET partial fills per symbol+stage.",
		},
		[]string{"symbol", "stage"},
	)

	// SigfailDetectionRunsTotal counts SIGFAIL detector ticks by outcome.
	// Labels: result — "ok" | "empty" | "err".
	SigfailDetectionRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_sigfail_detection_runs_total",
			Help: "v0.2 Round 3 SIGFAIL detector 5min ticks (ok/empty/err).",
		},
		[]string{"result"},
	)

	// SigfailDetectionsTotal counts trades closed via SIGFAIL per symbol+logic.
	// Labels: symbol, logic ("AND" | "OR"). Cardinality bounded by active symbols × 2.
	SigfailDetectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_sigfail_detections_total",
			Help: "v0.2 Round 3 SIGFAIL close fires per symbol+logic.",
		},
		[]string{"symbol", "logic"},
	)

	// v0.2 Round 4: User Data Stream metrics.
	UserStreamConnectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trader_user_stream_connected_total",
		Help: "WS user-stream successful connections (each Run loop iteration counts on success).",
	})
	UserStreamReconnectTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trader_user_stream_reconnect_total",
		Help: "WS user-stream session ended with error (next iteration reconnects).",
	})
	UserStreamKeepaliveErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trader_user_stream_keepalive_errors_total",
		Help: "PUT /fapi/v1/listenKey failures (next read likely fails → reconnect).",
	})
	UserStreamEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_user_stream_events_total",
			Help: "WS events received, labelled by event_type (ORDER_TRADE_UPDATE / ACCOUNT_UPDATE / MARGIN_CALL / etc.) plus 'raw' total.",
		},
		[]string{"event_type"},
	)

	// Round R.3: orphan algo cleaner metrics.
	OrphanAlgoTickTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orphan_algo_tick_total",
			Help: "orphan_algo_cleaner 1min ticks by result (ok/err).",
		},
		[]string{"result"},
	)
	OrphanAlgoCancelled = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orphan_algo_cancelled_total",
			Help: "Orphan algos cancelled per symbol+order_type (Round R.3).",
		},
		[]string{"symbol", "order_type"},
	)
	OrphanAlgoCancelFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trader_orphan_algo_cancel_failures_total",
			Help: "CancelAlgoOrder failures during orphan cleanup (will retry next tick).",
		},
		[]string{"symbol"},
	)
)

func init() {
	prometheus.MustRegister(
		CollectorRunsTotal, CollectorDurationSeconds, CollectorLastTickSeconds,
		SignalEvaluationsTotal,
		DecisionEvaluationsTotal, DecisionSizingDeviationPct,
		OrdersTotal, DisasterStopsPlacedTotal, OrderLatencySeconds,
		OrdersRetryTotal, OrdersIdempotentHitTotal, HaltAutoResetTotal,
		PositionSyncRunsTotal, PositionSyncDriftTotal, PositionMarginRatio, MarginCallTriggeredTotal,
		PositionDriftHaltTotal, PositionLocalOnlyOrphanTotal, PositionBinanceOnlyUnknownTotal, HaltRCAPendingTotal,
		ExitsTotal, ExitLatencySeconds, RealizedPnlTotal, PositionHoldDurationHours,
		CircuitBreakerTripsTotal, CircuitBreakerState, AccountBalanceUSDT, DailyPnlUSDT,
		ConsecutiveLossesGauge, BTC30MinDropPct, UnrealizedPnlTotalUSDT,
		RestartRecoveryRunsTotal,
		AlgoPollingRunsTotal, AlgoTriggeredTotal,
		TrailingStageUpgradeTotal, TPFilledTotal,
		SigfailDetectionRunsTotal, SigfailDetectionsTotal,
		UserStreamConnectedTotal, UserStreamReconnectTotal,
		UserStreamKeepaliveErrors, UserStreamEventsTotal,
		OrphanAlgoTickTotal, OrphanAlgoCancelled, OrphanAlgoCancelFailures,
	)
}
