package binance

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Per ARCH §11.5 Prometheus 指标. Counters register at package init; the
// gauge Collector registers per Pool via registerPoolMetrics (sync.Once
// — production has 1 Pool, multi-Pool tests bypass via direct Collect()).
var (
	proxyRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "binance_proxy_requests_total", Help: "Total proxy requests by URL and outcome."},
		[]string{"proxy_url", "outcome"},
	)
	proxyFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "binance_proxy_failures_total", Help: "Proxy failures by URL and error type."},
		[]string{"proxy_url", "error_type"},
	)
	activeCountDesc  = prometheus.NewDesc("binance_proxy_active_count", "Number of active (non-evicted) proxies in pool.", nil, nil)
	evictedCountDesc = prometheus.NewDesc("binance_proxy_evicted_count", "Number of evicted proxies in pool.", nil, nil)

	poolGaugeOnce sync.Once
)

func init() {
	prometheus.MustRegister(proxyRequestsTotal, proxyFailuresTotal)
}

// proxyMetricsCollector exposes Pool's ActiveCount / EvictedCount as gauges
// via the Collector pattern (vs Set+goroutine — gauge values are pulled
// on demand at scrape time, no background work).
type proxyMetricsCollector struct{ pool *Pool }

func (m *proxyMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- activeCountDesc
	ch <- evictedCountDesc
}

func (m *proxyMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	s := m.pool.Stats()
	ch <- prometheus.MustNewConstMetric(activeCountDesc, prometheus.GaugeValue, float64(s.ActiveCount))
	ch <- prometheus.MustNewConstMetric(evictedCountDesc, prometheus.GaugeValue, float64(s.EvictedCount))
}

// registerPoolMetrics registers the gauge Collector for Pool exactly once
// in the lifetime of the process. Production has a single Pool so this is
// fine; tests creating multiple Pools bypass the registry and call
// Collect() directly on a per-test proxyMetricsCollector.
func registerPoolMetrics(p *Pool) {
	poolGaugeOnce.Do(func() {
		prometheus.MustRegister(&proxyMetricsCollector{pool: p})
	})
}

// classifyError maps errors to a fixed 5-value label set for cardinality
// safety (raw err.Error() would explode the label space).
//
//	timeout — ctx deadline / *url.Error.Timeout()
//	5xx     — *APIError ≥500 or err.Error() contains "http 5"
//	4xx     — *APIError 4xx or err.Error() contains "http 4"
//	network — any other net.Error (refused / DNS / etc.)
//	other   — fallback
func classifyError(err error) string {
	if err == nil {
		return "other"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return "timeout"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPCode >= 500 {
			return "5xx"
		}
		if apiErr.HTTPCode >= 400 {
			return "4xx"
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "http 5") {
		return "5xx"
	}
	if strings.Contains(msg, "http 4") {
		return "4xx"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "network"
	}
	return "other"
}
