package backend

import (
	"context"
	"database/sql"
	"log/slog"
	"runtime/metrics"
	"slices"
	"time"
)

// healthSample owns its histogram counts: metrics.Read reuses the samples' storage.
type healthSample struct {
	at             time.Time
	allocatedBytes uint64
	heapBytes      uint64
	gcCPUSeconds   float64
	goroutines     uint64
	scheduler      metrics.Float64Histogram
	pool           sql.DBStats
}

func sampleHealth(samples []metrics.Sample, pool sql.DBStats) healthSample {
	metrics.Read(samples)
	histogram := samples[4].Value.Float64Histogram()

	return healthSample{
		at:             time.Now(),
		allocatedBytes: samples[0].Value.Uint64(),
		heapBytes:      samples[1].Value.Uint64(),
		gcCPUSeconds:   samples[2].Value.Float64(),
		goroutines:     samples[3].Value.Uint64(),
		scheduler:      metrics.Float64Histogram{Counts: slices.Clone(histogram.Counts), Buckets: histogram.Buckets},
		pool:           pool,
	}
}

// reportHealth runs until canceled; Run joins it before closing the sampled pool.
func reportHealth(ctx context.Context, db *sql.DB, logger *slog.Logger, samples []metrics.Sample, previous *healthSample) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := sampleHealth(samples, db.Stats())
			current.log(logger, previous)
			*previous = current
		}
	}
}

func (s *healthSample) log(logger *slog.Logger, previous *healthSample) {
	interval := s.at.Sub(previous.at).Seconds()
	attrs := []slog.Attr{
		slog.Float64("sample_interval_seconds", interval),
		slog.Uint64("go_heap_objects_bytes", s.heapBytes),
		slog.Float64("go_alloc_bytes_per_second", float64(s.allocatedBytes-previous.allocatedBytes)/interval),
		// The runtime estimates GC CPU consumption, not wall-clock pauses or OS CPU time.
		slog.Float64("go_gc_cpu_seconds_interval", s.gcCPUSeconds-previous.gcCPUSeconds),
		slog.Uint64("go_goroutines", s.goroutines),
		slog.Int("db_pool_in_use", s.pool.InUse),
		slog.Int("db_pool_idle", s.pool.Idle),
		slog.Int("db_pool_open", s.pool.OpenConnections),
		slog.Int64("db_pool_wait_count_interval", s.pool.WaitCount-previous.pool.WaitCount),
		// These waits are for pool connections, not SQL query execution.
		slog.Float64("db_pool_wait_duration_ms_interval", (s.pool.WaitDuration-previous.pool.WaitDuration).Seconds()*1000),
	}

	var total uint64
	for i, count := range s.scheduler.Counts {
		total += count - previous.scheduler.Counts[i]
	}

	attrs = append(attrs, slog.Uint64("go_scheduler_observations_interval", total))
	if total > 0 {
		// Use the nearest-rank p99's bucket upper bound, not a fabricated exact latency.
		rank := total - total/100

		var cumulative uint64
		for i, count := range s.scheduler.Counts {
			cumulative += count - previous.scheduler.Counts[i]
			if cumulative >= rank {
				attrs = append(attrs, slog.Float64("go_scheduler_p99_upper_seconds", s.scheduler.Buckets[i+1]))
				break
			}
		}
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "rocketclaw health", attrs...)
}
