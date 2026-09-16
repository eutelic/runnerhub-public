package fleet

import (
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestPoolRuntimeHealthStartsUnknown(t *testing.T) {
	now := time.Now().UTC()
	h := PoolRuntimeHealth("p", nil, now)
	if h.State != "unknown" {
		t.Fatalf("state=%q, want unknown", h.State)
	}
}

func TestPoolRuntimeHealthUsesSuccessfulRuntimeEvidence(t *testing.T) {
	now := time.Now().UTC()
	workers := []core.Worker{{
		PoolID: "p",
		State: core.WorkerDeleted,
		RegisteredAt: now.Add(-4 * time.Minute),
		StartedAt: now.Add(-3 * time.Minute),
		CompletedAt: now.Add(-2 * time.Minute),
	}}
	h := PoolRuntimeHealth("p", workers, now)
	if h.State != "healthy" || h.LastJobCompletedAt.IsZero() {
		t.Fatalf("health=%+v", h)
	}
}

func TestPoolRuntimeHealthMarksNewerFailureDegraded(t *testing.T) {
	now := time.Now().UTC()
	workers := []core.Worker{
		{PoolID: "p", State: core.WorkerDeleted, CompletedAt: now.Add(-10 * time.Minute)},
		{PoolID: "p", State: core.WorkerDeleted, FailedAt: now.Add(-time.Minute), FailureReason: "container exited"},
	}
	h := PoolRuntimeHealth("p", workers, now)
	if h.State != "degraded" || h.LastFailureReason != "container exited" || h.RecentFailures != 1 {
		t.Fatalf("health=%+v", h)
	}
}

func TestPoolRuntimeHealthBusyIsActive(t *testing.T) {
	now := time.Now().UTC()
	workers := []core.Worker{{
		PoolID: "p", State: core.WorkerBusy,
		RegisteredAt: now.Add(-time.Minute),
		StartedAt: now.Add(-30 * time.Second),
	}}
	h := PoolRuntimeHealth("p", workers, now)
	if h.State != "active" || h.BusyWorkers != 1 || h.ActiveWorkers != 1 {
		t.Fatalf("health=%+v", h)
	}
}

func TestPoolRuntimeHealthIgnoresOldFailuresInRecentCount(t *testing.T) {
	now := time.Now().UTC()
	workers := []core.Worker{{
		PoolID: "p", State: core.WorkerDeleted,
		FailedAt: now.Add(-48 * time.Hour),
		FailureReason: "old failure",
	}}
	h := PoolRuntimeHealth("p", workers, now)
	if h.RecentFailures != 0 || h.State != "degraded" {
		t.Fatalf("health=%+v", h)
	}
}
