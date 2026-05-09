package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorRunsTotal_IncrementsByLabel(t *testing.T) {
	CollectorRunsTotal.Reset()
	CollectorRunsTotal.WithLabelValues("oi", "success").Inc()
	CollectorRunsTotal.WithLabelValues("oi", "success").Inc()
	CollectorRunsTotal.WithLabelValues("oi", "error").Inc()
	CollectorRunsTotal.WithLabelValues("klines", "success").Inc()

	assert.EqualValues(t, 2, testutil.ToFloat64(CollectorRunsTotal.WithLabelValues("oi", "success")))
	assert.EqualValues(t, 1, testutil.ToFloat64(CollectorRunsTotal.WithLabelValues("oi", "error")))
	assert.EqualValues(t, 1, testutil.ToFloat64(CollectorRunsTotal.WithLabelValues("klines", "success")))
}

func TestCollectorRunsTotal_PanicOutcome(t *testing.T) {
	CollectorRunsTotal.Reset()
	CollectorRunsTotal.WithLabelValues("oi", "panic").Inc()
	assert.EqualValues(t, 1, testutil.ToFloat64(CollectorRunsTotal.WithLabelValues("oi", "panic")))
}

func TestCollectorDurationSeconds_ObservesByCollector(t *testing.T) {
	CollectorDurationSeconds.Reset()
	CollectorDurationSeconds.WithLabelValues("oi").Observe(0.5)
	CollectorDurationSeconds.WithLabelValues("oi").Observe(1.5)
	CollectorDurationSeconds.WithLabelValues("klines").Observe(0.1)

	// 2 distinct collectors registered → CollectAndCount returns 2 metric
	// rows (one histogram per collector label combination).
	require.Equal(t, 2, testutil.CollectAndCount(CollectorDurationSeconds, "trader_collector_duration_seconds"))
}

func TestMetrics_RegisteredInDefaultRegistry(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["trader_collector_runs_total"], "trader_collector_runs_total must be registered")
	assert.True(t, names["trader_collector_duration_seconds"], "trader_collector_duration_seconds must be registered")
}
