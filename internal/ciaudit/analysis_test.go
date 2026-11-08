package ciaudit

import (
	"math"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestSummarizeUnionsOverlappingWasteIntervals(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	job := JobExecution{
		ID:          "job-1",
		StartedAt:   base,
		CompletedAt: base.Add(10 * time.Minute),
		Resources:   core.Resources{CPUMillis: 2000, MemoryBytes: 4 << 30},
		Usage:       core.ResourceUsageSummary{AverageCPUMillis: 1000, AverageMemoryBytes: 2 << 30, Samples: 10},
	}
	findings := []Finding{
		{
			JobID:      job.ID,
			DetectorID: "post-merge-validation",
			Class:      EvidenceConfirmed,
			Interval:   Interval{Start: base.Add(2 * time.Minute), End: base.Add(10 * time.Minute)},
		},
		{
			JobID:      job.ID,
			DetectorID: "dependency",
			Class:      EvidenceDependencyProven,
			Interval:   Interval{Start: base, End: base.Add(10 * time.Minute)},
		},
		{
			JobID:      job.ID,
			DetectorID: "duplicate-overlap",
			Class:      EvidenceInputEquivalent,
			Interval:   Interval{Start: base.Add(4 * time.Minute), End: base.Add(9 * time.Minute)},
		},
	}

	got := Summarize([]JobExecution{job}, findings)

	assertNear(t, got.Total.RunnerSeconds, 600)
	assertNear(t, got.ConfirmedWaste.RunnerSeconds, 480)
	assertNear(t, got.ProvablyAvoidable.RunnerSeconds, 600)
	assertNear(t, got.Total.AllocatedCPUSeconds, 1200)
	assertNear(t, got.ConfirmedWaste.AllocatedCPUSeconds, 960)
	assertNear(t, got.ProvablyAvoidable.AllocatedRAMGiBSeconds, 2400)
	assertNear(t, got.ProvablyAvoidable.EstimatedCPUSeconds, 600)
	assertNear(t, got.ConfirmedWastePct, 80)
	assertNear(t, got.ProvablyAvoidablePct, 100)
}

func TestSummarizeExcludesAdvisoryFindings(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "job-1", StartedAt: base, CompletedAt: base.Add(time.Minute)}
	got := Summarize([]JobExecution{job}, []Finding{{
		JobID: "job-1", DetectorID: "similar-command", Class: EvidenceHighConfidence,
		Interval: Interval{Start: base, End: base.Add(time.Minute)},
	}})
	if got.ProvablyAvoidable.RunnerSeconds != 0 || got.ConfirmedWaste.RunnerSeconds != 0 {
		t.Fatalf("advisory finding counted as waste: %+v", got)
	}
}

func TestPostMergeFindingClipsToExecution(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "job-1", StartedAt: base.Add(10 * time.Minute), CompletedAt: base.Add(20 * time.Minute)}

	got, ok := PostMergeFinding(job, base.Add(5*time.Minute), true)
	if !ok {
		t.Fatal("expected finding")
	}
	if !got.Interval.Start.Equal(job.StartedAt) || !got.Interval.End.Equal(job.CompletedAt) {
		t.Fatalf("interval=%+v", got.Interval)
	}

	if _, ok := PostMergeFinding(job, base.Add(5*time.Minute), false); ok {
		t.Fatal("side-effecting job must not be classified as confirmed waste")
	}
}

func TestSupersededFindingCountsOnlyTimeAfterSupersession(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "job-1", StartedAt: base, CompletedAt: base.Add(10 * time.Minute)}
	got, ok := SupersededFinding(job, base.Add(6*time.Minute), true)
	if !ok {
		t.Fatal("expected finding")
	}
	if got.Interval.Start != base.Add(6*time.Minute) || got.Interval.End != job.CompletedAt {
		t.Fatalf("interval=%+v", got.Interval)
	}
}

func TestSummarizeClipsFindingsToJobRuntime(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "job-1", StartedAt: base, CompletedAt: base.Add(5 * time.Minute)}
	got := Summarize([]JobExecution{job}, []Finding{{
		JobID: "job-1", DetectorID: "wide", Class: EvidenceConfirmed,
		Interval: Interval{Start: base.Add(-time.Hour), End: base.Add(time.Hour)},
	}})
	assertNear(t, got.ConfirmedWaste.RunnerSeconds, 300)
}

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExecutionFromWorkerUsesExistingTelemetry(t *testing.T) {
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	worker := core.Worker{
		ID:            "worker-1",
		Repository:    "eutelic/runnerhub",
		WorkflowRef:   ".github/workflows/ci.yml@refs/heads/main",
		WorkflowRunID: 123,
		JobID:         "job-456",
		StartedAt:     base,
		CompletedAt:   base.Add(2 * time.Minute),
		Resources:     core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30},
		Usage:         core.ResourceUsageSummary{AverageCPUMillis: 1250, AverageMemoryBytes: 3 << 30, Samples: 8},
	}
	got, ok := ExecutionFromWorker(worker)
	if !ok {
		t.Fatal("expected valid execution")
	}
	if got.ID != worker.ID || got.Repository != worker.Repository || got.WorkflowRunID != worker.WorkflowRunID || got.GitHubJobID != worker.JobID || got.Resources != worker.Resources || got.Usage.Samples != worker.Usage.Samples {
		t.Fatalf("execution=%+v", got)
	}
}

func TestPotentialSavingsRemainSeparateFromProvableWaste(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "job-potential", StartedAt: base, CompletedAt: base.Add(10 * time.Minute)}
	got := Summarize([]JobExecution{job}, []Finding{{
		JobID: "job-potential", DetectorID: "duplicate-artifact-build", Class: EvidenceHighConfidence,
		Interval: Interval{Start: base, End: base.Add(4 * time.Minute)},
	}})
	assertNear(t, got.PotentialSavings.RunnerSeconds, 240)
	assertNear(t, got.PotentialSavingsPct, 40)
	assertNear(t, got.ProvablyAvoidable.RunnerSeconds, 0)
	assertNear(t, got.ConfirmedWaste.RunnerSeconds, 0)
}

func TestStaleSnapshotPublicationFinding(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "publish", StartedAt: base, CompletedAt: base.Add(20 * time.Minute)}
	got, ok := StaleSnapshotPublicationFinding(job, base.Add(7*time.Minute), true)
	if !ok {
		t.Fatal("expected stale publication finding")
	}
	if got.DetectorID != "stale-snapshot-publication" || got.Class != EvidenceConfirmed {
		t.Fatalf("finding=%+v", got)
	}
	if got.Interval.Start != base.Add(7*time.Minute) || got.Interval.End != job.CompletedAt {
		t.Fatalf("interval=%+v", got.Interval)
	}
	if _, ok := StaleSnapshotPublicationFinding(job, base.Add(7*time.Minute), false); ok {
		t.Fatal("immutable release publication must not be classified as stale snapshot waste")
	}
}

func TestSummarizeDoesNotDoubleCountReplayedWorkerHistory(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "worker-replayed", StartedAt: base, CompletedAt: base.Add(2 * time.Minute), Resources: core.Resources{CPUMillis: 2000}}
	finding := Finding{JobID: job.ID, DetectorID: "post-merge-validation", Class: EvidenceConfirmed, Interval: Interval{Start: base.Add(time.Minute), End: job.CompletedAt}}
	got := Summarize([]JobExecution{job, job}, []Finding{finding})
	assertNear(t, got.Total.RunnerSeconds, 120)
	assertNear(t, got.Total.AllocatedCPUSeconds, 240)
	assertNear(t, got.ConfirmedWaste.RunnerSeconds, 60)
	assertNear(t, got.ConfirmedWastePct, 50)
}

func TestSummarizePrefersCompleteReplayRegardlessOfOrder(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	partial := JobExecution{ID: "worker-replayed", StartedAt: base, CompletedAt: base.Add(time.Minute)}
	complete := partial
	complete.CompletedAt = base.Add(3 * time.Minute)
	finding := Finding{JobID: partial.ID, Class: EvidenceConfirmed, Interval: Interval{Start: base.Add(2 * time.Minute), End: complete.CompletedAt}}
	for _, jobs := range [][]JobExecution{{partial, complete}, {complete, partial}} {
		got := Summarize(jobs, []Finding{finding})
		assertNear(t, got.Total.RunnerSeconds, 180)
		assertNear(t, got.ConfirmedWaste.RunnerSeconds, 60)
		assertNear(t, got.ConfirmedWastePct, 100.0/3.0)
	}
}

func TestSummarizeReplaySelectionIndependentOfOrder(t *testing.T) {
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	old := JobExecution{ID: "replayed", StartedAt: base, CompletedAt: base.Add(time.Minute), Resources: core.Resources{CPUMillis: 1000}}
	newer := old
	newer.StartedAt = base.Add(time.Minute)
	newer.CompletedAt = base.Add(3 * time.Minute)
	newer.Resources.CPUMillis = 2000
	for _, jobs := range [][]JobExecution{{old, newer}, {newer, old}} {
		got := Summarize(jobs, nil)
		assertNear(t, got.Total.RunnerSeconds, 120)
		assertNear(t, got.Total.AllocatedCPUSeconds, 240)
	}
}

func TestSummarizeReplayPrefersMoreTelemetryAtEqualCompletion(t *testing.T) {
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	partial := JobExecution{ID: "replayed", StartedAt: base, CompletedAt: base.Add(time.Minute)}
	complete := partial
	complete.Usage = core.ResourceUsageSummary{Samples: 4, AverageCPUMillis: 500}
	for _, jobs := range [][]JobExecution{{partial, complete}, {complete, partial}} {
		got := Summarize(jobs, nil)
		assertNear(t, got.Total.EstimatedCPUSeconds, 30)
	}
}
