package backend

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"math"
	"runtime/metrics"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestHealthReportCadenceAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A zero pool's Stats requires no driver, DB I/O, or external goroutines.
		db := new(sql.DB)

		var logs lockedBuffer

		logger := slog.New(slog.NewTextHandler(&logs, nil)).With("process_start", "test-start")
		samples := []metrics.Sample{
			{Name: "/gc/heap/allocs:bytes"},
			{Name: "/memory/classes/heap/objects:bytes"},
			{Name: "/cpu/classes/gc/total:cpu-seconds"},
			{Name: "/sched/goroutines:goroutines"},
			{Name: "/sched/latencies:seconds"},
		}
		baseline := sampleHealth(samples, db.Stats())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var reporter errgroup.Group
		reporter.Go(func() error {
			reportHealth(ctx, db, logger, samples, &baseline)
			return nil
		})
		synctest.Wait()
		time.Sleep(time.Minute - time.Nanosecond)
		require.Empty(t, logs.String())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, strings.Count(logs.String(), "rocketclaw health"))
		require.Contains(t, logs.String(), "level=INFO")
		require.Contains(t, logs.String(), "sample_interval_seconds=60")
		require.Contains(t, logs.String(), "process_start=test-start")
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, strings.Count(logs.String(), "rocketclaw health"))
		cancel()
		require.NoError(t, reporter.Wait())

		before := logs.String()

		time.Sleep(time.Minute)
		require.Equal(t, before, logs.String())
	})
}

func TestHealthReportKnownIntervalDeltas(t *testing.T) {
	previous := healthSample{
		at: time.Now(), allocatedBytes: 1000, gcCPUSeconds: 2,
		pool:      sql.DBStats{WaitCount: 20, WaitDuration: time.Second},
		scheduler: metrics.Float64Histogram{Counts: []uint64{1000, 20, 10}},
	}
	current := healthSample{
		at: previous.at.Add(90 * time.Second), allocatedBytes: 1900, heapBytes: 700, gcCPUSeconds: 2.25, goroutines: 12,
		pool:      sql.DBStats{InUse: 3, Idle: 2, OpenConnections: 5, WaitCount: 23, WaitDuration: 1250 * time.Millisecond},
		scheduler: metrics.Float64Histogram{Buckets: []float64{0, .001, .01, math.Inf(1)}, Counts: []uint64{1000, 120, 10}},
	}

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))
	current.log(logger, &previous)

	for _, field := range []string{
		"sample_interval_seconds=90", "go_alloc_bytes_per_second=10", "go_heap_objects_bytes=700",
		"go_gc_cpu_seconds_interval=0.25", "go_goroutines=12", "db_pool_in_use=3", "db_pool_idle=2", "db_pool_open=5",
		"db_pool_wait_count_interval=3", "db_pool_wait_duration_ms_interval=250",
		"go_scheduler_observations_interval=100", "go_scheduler_p99_upper_seconds=0.01",
	} {
		require.Contains(t, logs.String(), field)
	}

	logs.Reset()

	previous = current
	current.at = current.at.Add(time.Minute)
	current.log(logger, &previous)
	require.Contains(t, logs.String(), "go_scheduler_observations_interval=0")
	require.NotContains(t, logs.String(), "go_scheduler_p99")
	require.Contains(t, logs.String(), "go_alloc_bytes_per_second=0")
	require.Contains(t, logs.String(), "go_gc_cpu_seconds_interval=0")
	require.Contains(t, logs.String(), "db_pool_wait_count_interval=0")
	require.Contains(t, logs.String(), "db_pool_wait_duration_ms_interval=0")
}

func TestHealthSamplePreservesHistogramBackingCounts(t *testing.T) {
	samples := []metrics.Sample{
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/sched/latencies:seconds"},
	}
	previous := sampleHealth(samples, sql.DBStats{})
	counts := samples[4].Value.Float64Histogram().Counts
	original := previous.scheduler.Counts[0]
	counts[0]++ // Emulate the next metrics.Read reusing its backing storage.

	require.Equal(t, original, previous.scheduler.Counts[0])

	current := sampleHealth(samples, sql.DBStats{})
	require.NotEmpty(t, current.scheduler.Buckets)
	current.scheduler.Counts[0]++
	require.Equal(t, original, previous.scheduler.Counts[0])
}

func BenchmarkHealthSampleAndLog(b *testing.B) {
	db := new(sql.DB)
	samples := []metrics.Sample{
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/sched/latencies:seconds"},
	}
	previous := sampleHealth(samples, db.Stats())
	previous.at = previous.at.Add(-time.Minute)

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil)).With("process_start", time.Now().UTC().Format(time.RFC3339Nano))
	current := sampleHealth(samples, db.Stats())
	current.scheduler.Counts[0]++
	current.log(logger, &previous)

	logBytes := logs.Len()

	b.ReportAllocs()

	for b.Loop() {
		current = sampleHealth(samples, db.Stats())
		current.scheduler.Counts[0]++

		logs.Reset()
		current.log(logger, &previous)
	}

	b.ReportMetric(float64(logBytes), "log-bytes/report")
}
