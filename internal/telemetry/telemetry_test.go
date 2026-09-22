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


func TestRecommendResourcesUsesDinDPerContainerEnvelope(t *testing.T) {
	pool := core.RunnerPool{
		ID: "publisher",
		Isolation: core.IsolationContainerDinD,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30},
	}
	base := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 5; i++ {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("dind-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerDinD,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: 4800, PeakMemoryBytes: 12 << 30,
				PeakEnvelopeCPUMillis: 2400, PeakEnvelopeMemoryBytes: 7 << 30, Samples: 3,
			},
		})
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.Jobs != 5 {
		t.Fatalf("recommendation=%+v", got)
	}
	if got.CPUPeakP95Millis != 2400 || got.MemoryPeakP95Bytes != 7<<30 {
		t.Fatalf("DinD sizing used aggregate instead of envelope: %+v", got)
	}
	if got.Recommended.CPUMillis != 3000 || got.Recommended.MemoryBytes != 9<<30 {
		t.Fatalf("DinD recommendation=%+v, want 3000m / 9GiB", got.Recommended)
	}
}

func TestRecommendResourcesRejectsLegacyDinDAggregateSamples(t *testing.T) {
	pool := core.RunnerPool{
		ID: "publisher",
		Isolation: core.IsolationContainerDinD,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30},
	}
	base := time.Date(2026, 9, 21, 2, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 5; i++ {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("legacy-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerDinD,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{PeakCPUMillis: 12070, PeakMemoryBytes: 2 << 30, Samples: 2},
		})
	}
	got := RecommendResources(pool, workers)
	if got.Ready || got.Jobs != 0 || got.RejectedJobs != 5 {
		t.Fatalf("legacy DinD aggregate samples were trusted: %+v", got)
	}
}

func TestRecommendResourcesRejectsImpossibleTrustedSample(t *testing.T) {
	pool := core.RunnerPool{
		ID: "standard",
		Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
	}
	base := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 5; i++ {
		cpu := int64(1500)
		if i == 4 {
			cpu = 12070
		}
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("trusted-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: cpu, PeakMemoryBytes: 2 << 30,
				PeakEnvelopeCPUMillis: cpu, PeakEnvelopeMemoryBytes: 2 << 30, Samples: 2,
			},
		})
	}
	got := RecommendResources(pool, workers)
	if got.Ready || got.Jobs != 4 || got.RejectedJobs != 1 {
		t.Fatalf("impossible sample was not rejected: %+v", got)
	}
}

func TestRecommendResourcesHonorsLearningReset(t *testing.T) {
	resetAt := time.Date(2026, 9, 21, 4, 30, 0, 0, time.UTC)
	pool := core.RunnerPool{
		ID: "reset", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
		LearningResetAt: resetAt,
	}
	var workers []core.Worker
	for i := 0; i < 10; i++ {
		completed := resetAt.Add(time.Duration(i-5) * time.Minute)
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("reset-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
			State: core.WorkerDeleted, StartedAt: completed.Add(-30 * time.Second), CompletedAt: completed,
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: 1000, PeakMemoryBytes: 2 << 30,
				PeakEnvelopeCPUMillis: 1000, PeakEnvelopeMemoryBytes: 2 << 30, Samples: 2,
			},
		})
	}
	workers = append(workers, core.Worker{
		ID: "inflight-before-reset", PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
		State: core.WorkerDeleted, CreatedAt: resetAt.Add(-10 * time.Minute), StartedAt: resetAt.Add(-5 * time.Minute),
		CompletedAt: resetAt.Add(5 * time.Minute), JobResult: "succeeded", Resources: pool.Resources,
		Usage: core.ResourceUsageSummary{
			PeakCPUMillis: 2400, PeakMemoryBytes: 3 << 30,
			PeakEnvelopeCPUMillis: 2400, PeakEnvelopeMemoryBytes: 3 << 30, Samples: 2,
		},
	})
	got := RecommendResources(pool, workers)
	// Completion exactly at reset time and a worker already in flight at reset
	// are excluded, leaving four workers that actually started after reset.
	if got.Jobs != 4 || got.Ready {
		t.Fatalf("reset cutoff not honored: %+v", got)
	}
	if got.ResetAt == nil || !got.ResetAt.Equal(resetAt) {
		t.Fatalf("resetAt=%v, want %v", got.ResetAt, resetAt)
	}
}


func TestRecommendResourcesTreatsOOMAsMemoryLimitPressure(t *testing.T) {
	pool := core.RunnerPool{
		ID: "oom-pressure", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
	}
	base := time.Date(2026, 9, 21, 5, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 5; i++ {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("oom-pressure-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			FailedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "failed", ProviderOOMKilled: true, Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: 1000, PeakMemoryBytes: 2 << 30,
				PeakEnvelopeCPUMillis: 1000, PeakEnvelopeMemoryBytes: 2 << 30, Samples: 2,
			},
		})
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.MemoryPeakP95Bytes != 4<<30 {
		t.Fatalf("OOM did not promote memory pressure to configured limit: %+v", got)
	}
	if got.Recommended.MemoryBytes != 5<<30 {
		t.Fatalf("OOM recommendation memory=%d, want %d", got.Recommended.MemoryBytes, int64(5<<30))
	}
}


func TestRecommendResourcesDoesNotDownsizeSparseHistory(t *testing.T) {
	pool := core.RunnerPool{ID: "sparse", Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}}
	base := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 30; i++ {
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("sparse-%d", i), PoolID: pool.ID, State: core.WorkerDeleted,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 10*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{PeakCPUMillis: 500, PeakMemoryBytes: 1 << 30, Samples: 1},
		})
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.Jobs != 30 || got.WellSampledJobs != 0 {
		t.Fatalf("sparse recommendation metadata=%+v", got)
	}
	if got.Recommended.CPUMillis != pool.Resources.CPUMillis || got.Recommended.MemoryBytes != pool.Resources.MemoryBytes {
		t.Fatalf("sparse history downsized pool: %+v", got)
	}
}


func TestRecommendResourcesRareOOMCannotBeHiddenByP95(t *testing.T) {
	pool := core.RunnerPool{
		ID: "rare-oom", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
	}
	base := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 50; i++ {
		oom := i == 49
		wk := core.Worker{
			ID: fmt.Sprintf("rare-oom-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: 1000, PeakMemoryBytes: 1 << 30,
				PeakEnvelopeCPUMillis: 1000, PeakEnvelopeMemoryBytes: 1 << 30, Samples: 2,
			},
		}
		if oom {
			wk.CompletedAt = time.Time{}
			wk.FailedAt = base.Add(time.Duration(i)*time.Minute + 30*time.Second)
			wk.JobResult = "failed"
			wk.ProviderOOMKilled = true
		}
		workers = append(workers, wk)
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.MemoryPeakP95Bytes != 1<<30 {
		t.Fatalf("unexpected p95 metadata: %+v", got)
	}
	if got.Recommended.MemoryBytes != 5<<30 {
		t.Fatalf("rare OOM was hidden by p95: recommendation=%+v", got)
	}
}


func TestRecommendResourcesRareMemoryHeavyJobCannotBeHiddenByP95(t *testing.T) {
	pool := core.RunnerPool{
		ID: "rare-memory", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
	}
	base := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	var workers []core.Worker
	for i := 0; i < 50; i++ {
		memory := int64(1 << 30)
		if i == 49 {
			memory = 7 << 29 // 3.5 GiB
		}
		workers = append(workers, core.Worker{
			ID: fmt.Sprintf("rare-memory-%d", i), PoolID: pool.ID, Isolation: core.IsolationContainerTrusted,
			State: core.WorkerDeleted, StartedAt: base.Add(time.Duration(i) * time.Minute),
			CompletedAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			JobResult: "succeeded", Resources: pool.Resources,
			Usage: core.ResourceUsageSummary{
				PeakCPUMillis: 1000, PeakMemoryBytes: memory,
				PeakEnvelopeCPUMillis: 1000, PeakEnvelopeMemoryBytes: memory, Samples: 2,
			},
		})
	}
	got := RecommendResources(pool, workers)
	if !got.Ready || got.MemoryPeakP95Bytes != 1<<30 || got.MemoryPeakMaxBytes != 7<<29 {
		t.Fatalf("unexpected memory sizing metadata: %+v", got)
	}
	if got.Recommended.MemoryBytes != 9<<29 { // 4.5 GiB after 25% headroom and 0.5 GiB rounding
		t.Fatalf("rare memory-heavy job was hidden by p95: recommendation=%+v", got)
	}
}
