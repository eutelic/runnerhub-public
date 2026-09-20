package telemetry

import (
	"fmt"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestQueueMetricsArePoolSpecific(t *testing.T) {
	var m Metrics
	m.Queue("fast", 2*time.Second)
	m.Queue("fast", 4*time.Second)
	m.Queue("slow", 90*time.Second)

	fast := m.PoolSnapshot("fast")
	slow := m.PoolSnapshot("slow")
	if fast.QueueSamples != 2 || fast.QueueTotal != 6*time.Second {
		t.Fatalf("fast snapshot=%+v", fast)
	}
	if slow.QueueSamples != 1 || slow.QueueTotal != 90*time.Second {
		t.Fatalf("slow snapshot=%+v", slow)
	}

	global := m.Snapshot()
	if global.QueueSamples != 3 || global.QueueTotal != 96*time.Second {
		t.Fatalf("global snapshot=%+v", global)
	}
}

func TestQueueMetricsPoolTrackingIsBounded(t *testing.T) {
	var m Metrics
	for i := 0; i < maxTrackedPools+32; i++ {
		m.Queue(fmt.Sprintf("pool-%03d", i), time.Second)
	}
	if len(m.pools) != maxTrackedPools {
		t.Fatalf("tracked pools=%d, want %d", len(m.pools), maxTrackedPools)
	}
	if _, ok := m.pools["pool-000"]; ok {
		t.Fatal("oldest pool metric was not evicted")
	}
	if _, ok := m.pools[fmt.Sprintf("pool-%03d", maxTrackedPools+31)]; !ok {
		t.Fatal("newest pool metric missing")
	}
}


func TestRecommendResourcesUsesP95PeakWithHeadroom(t *testing.T) {
	pool := core.RunnerPool{
		ID: "p1",
		Resources: core.Resources{CPUMillis: 2000, MemoryBytes: 4 << 30, DiskBytes: 20 << 30},
	}
	cpuPeaks := []int64{1000, 1200, 1400, 1600, 2000}
	memoryPeaks := []int64{2 << 30, 5 << 29, 3 << 30, 7 << 29, 4 << 30}
	workers := make([]core.Worker, 0, len(cpuPeaks))
	base := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC)
	for i := range cpuPeaks {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("w-%d", i),
			PoolID: pool.ID,
			State: core.WorkerDeleted,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: cpuPeaks[i],
				PeakMemoryBytes: memoryPeaks[i],
				Samples: 2,
			},
		})
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.Jobs != 5 {
		t.Fatalf("recommendation not ready: %+v", got)
	}
	if got.CPUPeakP95Millis != 2000 {
		t.Fatalf("cpu p95=%d, want 2000", got.CPUPeakP95Millis)
	}
	if got.Recommended.CPUMillis != 2500 {
		t.Fatalf("recommended cpu=%d, want 2500", got.Recommended.CPUMillis)
	}
	if got.MemoryPeakP95Bytes != 4<<30 {
		t.Fatalf("memory p95=%d, want %d", got.MemoryPeakP95Bytes, int64(4<<30))
	}
	if got.Recommended.MemoryBytes != 5<<30 {
		t.Fatalf("recommended memory=%d, want %d", got.Recommended.MemoryBytes, int64(5<<30))
	}
}

func TestRecommendResourcesWaitsForEnoughJobs(t *testing.T) {
	pool := core.RunnerPool{ID: "p1", Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}}
	workers := []core.Worker{{
		PoolID: pool.ID,
		State: core.WorkerDeleted,
		StartedAt: time.Now().Add(-time.Minute),
		CompletedAt: time.Now(),
		Usage: core.ResourceUsageSummary{PeakCPUMillis: 1000, PeakMemoryBytes: 2 << 30, Samples: 1},
	}}
	got := RecommendResources(pool, workers)
	if got.Ready {
		t.Fatalf("recommendation should still be collecting: %+v", got)
	}
	if got.Recommended.CPUMillis != pool.Resources.CPUMillis || got.Recommended.MemoryBytes != pool.Resources.MemoryBytes {
		t.Fatalf("resource limits changed before enough samples: %+v", got)
	}
}


func TestRecommendResourcesIgnoresOrdinaryFailedJobsButKeepsOOMPressure(t *testing.T) {
	pool := core.RunnerPool{ID: "p1", Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}}
	base := time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 5; i++ {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("ok-%d", i), PoolID: pool.ID, State: core.WorkerDeleted,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded",
			Usage: core.ResourceUsageSummary{PeakCPUMillis: 1000, PeakMemoryBytes: 2 << 30, Samples: 2},
		})
	}
	workers = append(workers,
		core.Worker{
			ID: "early-failure", PoolID: pool.ID, State: core.WorkerDeleted,
			StartedAt: base.Add(10 * time.Minute), FailedAt: base.Add(10*time.Minute + 5*time.Second),
			JobResult: "failed",
			Usage: core.ResourceUsageSummary{PeakCPUMillis: 100, PeakMemoryBytes: 128 << 20, Samples: 1},
		},
		core.Worker{
			ID: "oom", PoolID: pool.ID, State: core.WorkerDeleted,
			StartedAt: base.Add(11 * time.Minute), FailedAt: base.Add(11*time.Minute + 20*time.Second),
			JobResult: "failed", ProviderOOMKilled: true,
			Usage: core.ResourceUsageSummary{PeakCPUMillis: 1000, PeakMemoryBytes: 7 << 30, Samples: 2},
		},
	)
	got := RecommendResources(pool, workers)
	if !got.Ready || got.Jobs != 6 {
		t.Fatalf("eligible jobs=%d ready=%v, want 6 and ready", got.Jobs, got.Ready)
	}
	if got.MemoryPeakP95Bytes != 7<<30 {
		t.Fatalf("memory p95=%d, want OOM pressure %d", got.MemoryPeakP95Bytes, int64(7<<30))
	}
}


func TestRecommendResourcesDownsizesOnlyWithHighConfidenceHistory(t *testing.T) {
	pool := core.RunnerPool{ID: "p1", Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}}
	base := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	makeWorkers := func(n int) []core.Worker {
		workers := make([]core.Worker, 0, n)
		for i := 0; i < n; i++ {
			workers = append(workers, core.Worker{
				ID: fmt.Sprintf("light-%d", i), PoolID: pool.ID, State: core.WorkerDeleted,
				StartedAt: base.Add(time.Duration(i) * time.Minute),
				CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
				JobResult: "succeeded",
				Usage: core.ResourceUsageSummary{PeakCPUMillis: 1000, PeakMemoryBytes: 2 << 30, Samples: 2},
			})
		}
		return workers
	}
	early := RecommendResources(pool, makeWorkers(5))
	if early.Recommended.CPUMillis != pool.Resources.CPUMillis || early.Recommended.MemoryBytes != pool.Resources.MemoryBytes {
		t.Fatalf("downsized too early: %+v", early)
	}
	mature := RecommendResources(pool, makeWorkers(25))
	if mature.Recommended.CPUMillis != 1250 || mature.Recommended.MemoryBytes != 5<<29 {
		t.Fatalf("mature recommendation=%+v, want 1250m / 2.5GiB", mature)
	}
}
