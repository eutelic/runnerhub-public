package api

import (
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestRecentPoolQueueReportsCoverageAndZeroWait(t *testing.T) {
	now := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	workers := []core.Worker{
		{ID: "zero", PoolID: "p", StartedAt: started, QueuedAt: started},
		{ID: "short", PoolID: "p", StartedAt: started, QueuedAt: started.Add(-30 * time.Second)},
		{ID: "long", PoolID: "p", StartedAt: started, QueuedAt: started.Add(-120 * time.Second)},
		{ID: "missing", PoolID: "p", StartedAt: started},
		{ID: "invalid", PoolID: "p", StartedAt: started, QueuedAt: started.Add(time.Second)},
		{ID: "old", PoolID: "p", StartedAt: now.Add(-8 * 24 * time.Hour), QueuedAt: now.Add(-8*24*time.Hour - time.Minute)},
		{ID: "future", PoolID: "p", StartedAt: now.Add(time.Minute)},
		{ID: "other", PoolID: "other", StartedAt: started, QueuedAt: started.Add(-time.Second)},
		{ID: "unstarted", PoolID: "p"},
	}
	q := summarizeRecentPoolQueue("p", workers, now)
	if q.StartedJobs != 5 || q.Samples != 3 || q.CoveragePercent != 60 {
		t.Fatalf("incorrect queue coverage: %+v", q)
	}
	if q.P50Seconds == nil || *q.P50Seconds != 30 ||
		q.P95Seconds == nil || *q.P95Seconds != 120 {
		t.Fatalf("incorrect queue percentiles: %+v", q)
	}
}

func TestRecentPoolQueueUnknownIsNotZero(t *testing.T) {
	now := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	samples := []core.Worker{{PoolID: "p", StartedAt: now.Add(-time.Minute)}}
	q := summarizeRecentPoolQueue("p", samples, now)
	if q.Samples != 0 || q.StartedJobs != 1 || q.P50Seconds != nil || q.P95Seconds != nil {
		t.Fatalf("missing queue evidence was treated as zero delay: %+v", q)
	}
}
