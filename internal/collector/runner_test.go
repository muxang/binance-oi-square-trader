package collector

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCollector lets tests inject behavior into Run.
type fakeCollector struct {
	name string
	fn   func(ctx context.Context) error
}

func (f *fakeCollector) Name() string                  { return f.name }
func (f *fakeCollector) Run(ctx context.Context) error { return f.fn(ctx) }

// captureMetrics replaces the package metric hooks with atomic counters and
// restores them on cleanup. Returns pointers so tests can read counts.
func captureMetrics(t *testing.T) (started, success, failed *atomic.Int32) {
	t.Helper()
	started, success, failed = &atomic.Int32{}, &atomic.Int32{}, &atomic.Int32{}
	sv1, sv2, sv3 := metricStarted, metricSuccess, metricFailed
	metricStarted = func(string) { started.Add(1) }
	metricSuccess = func(string) { success.Add(1) }
	metricFailed = func(string, string) { failed.Add(1) }
	t.Cleanup(func() {
		metricStarted, metricSuccess, metricFailed = sv1, sv2, sv3
	})
	return
}

func TestRunner_Register_AddsCron(t *testing.T) {
	r := New(zerolog.Nop())
	err := r.Register(&fakeCollector{name: "t", fn: func(context.Context) error { return nil }}, "@every 1s")
	require.NoError(t, err)
	assert.Len(t, r.cron.Entries(), 1)
}

func TestRunner_Register_BadSchedule(t *testing.T) {
	r := New(zerolog.Nop())
	err := r.Register(&fakeCollector{name: "t", fn: func(context.Context) error { return nil }}, "not-a-schedule")
	require.Error(t, err)
}

func TestRunner_Run_Success_LogsAndIncrementsMetric(t *testing.T) {
	_, success, failed := captureMetrics(t)
	r := New(zerolog.Nop())
	r.run(&fakeCollector{name: "t", fn: func(context.Context) error { return nil }})
	assert.EqualValues(t, 1, success.Load())
	assert.EqualValues(t, 0, failed.Load())
}

func TestRunner_Run_Error_IncrementsErrorMetric(t *testing.T) {
	_, success, failed := captureMetrics(t)
	r := New(zerolog.Nop())
	r.run(&fakeCollector{name: "t", fn: func(context.Context) error { return errors.New("boom") }})
	assert.EqualValues(t, 0, success.Load())
	assert.EqualValues(t, 1, failed.Load())
}

// TestRunner_Run_Panic_RecoveryPreventsCronCrash is the safety-critical case:
// a panicking collector must not propagate up to the cron scheduler goroutine
// (which would tear down all collectors).
func TestRunner_Run_Panic_RecoveryPreventsCronCrash(t *testing.T) {
	_, _, failed := captureMetrics(t)
	r := New(zerolog.Nop())
	require.NotPanics(t, func() {
		r.run(&fakeCollector{name: "t", fn: func(context.Context) error { panic("boom") }})
	})
	assert.EqualValues(t, 1, failed.Load())
}

// TestRunner_Run_Timeout_CtxHasDeadline pins the per-tick deadline contract.
// We don't wait for the real 4-minute timeout — we just verify the collector
// receives a ctx with a deadline (so any deadline-aware collector will respect
// it).
func TestRunner_Run_Timeout_CtxHasDeadline(t *testing.T) {
	captureMetrics(t)
	var hadDeadline atomic.Bool
	r := New(zerolog.Nop())
	r.run(&fakeCollector{
		name: "t",
		fn: func(ctx context.Context) error {
			_, ok := ctx.Deadline()
			hadDeadline.Store(ok)
			return nil
		},
	})
	assert.True(t, hadDeadline.Load(), "collector ctx must have a deadline (PerTickTimeout)")
}

func TestRunner_Stop_WaitsForRunningCollectors(t *testing.T) {
	captureMetrics(t)
	r := New(zerolog.Nop())
	var running atomic.Bool
	var done atomic.Bool
	err := r.Register(&fakeCollector{
		name: "slow",
		fn: func(ctx context.Context) error {
			running.Store(true)
			select {
			case <-time.After(80 * time.Millisecond):
				done.Store(true)
			case <-ctx.Done():
			}
			return nil
		},
	}, "@every 50ms")
	require.NoError(t, err)
	r.Start()
	for i := 0; i < 100 && !running.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, r.Stop(2*time.Second))
	assert.True(t, done.Load(), "Stop should wait for in-flight collector to finish")
}

func TestRunner_Stop_TimeoutCutsOff(t *testing.T) {
	// captureMetrics intentionally NOT used: this test starts a goroutine
	// that ignores ctx and runs past Stop's timeout (then unblocks via
	// `defer close(block)`). It eventually calls metricSuccess; if the
	// metric vars were swapped via t.Cleanup, that would race with the
	// late call. We only assert Stop's return value here, no counters.
	r := New(zerolog.Nop())
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	defer close(block)
	err := r.Register(&fakeCollector{
		name: "stuck",
		fn: func(ctx context.Context) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-block // ignores ctx — simulates a wedged collector
			return nil
		},
	}, "@every 30ms")
	require.NoError(t, err)
	r.Start()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("collector never started")
	}
	err = r.Stop(50 * time.Millisecond)
	require.Error(t, err, "Stop must time out when collector ignores ctx")
}
