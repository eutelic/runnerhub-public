package fleet

import (
	"sort"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/scheduler"
)

type RuntimeHealth struct {
	PoolID             string    `json:"poolId"`
	State              string    `json:"state"`
	ActiveWorkers      int       `json:"activeWorkers"`
	BusyWorkers        int       `json:"busyWorkers"`
	RecentFailures     int       `json:"recentFailures"`
	LastRegisteredAt   time.Time `json:"lastRegisteredAt,omitempty"`
	LastJobStartedAt   time.Time `json:"lastJobStartedAt,omitempty"`
	LastJobCompletedAt time.Time `json:"lastJobCompletedAt,omitempty"`
	LastFailureAt      time.Time `json:"lastFailureAt,omitempty"`
	LastFailureReason  string    `json:"lastFailureReason,omitempty"`
}

func RuntimeHealthByPool(pools []core.RunnerPool, workers []core.Worker, now time.Time) []RuntimeHealth {
	out := make([]RuntimeHealth, 0, len(pools))
	for _, pool := range pools {
		out = append(out, PoolRuntimeHealth(pool.ID, workers, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PoolID < out[j].PoolID })
	return out
}

func jobSucceeded(result string) bool {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "success", "succeeded":
		return true
	default:
		return false
	}
}

func PoolRuntimeHealth(poolID string, workers []core.Worker, now time.Time) RuntimeHealth {
	h := RuntimeHealth{PoolID: poolID, State: "unknown"}
	cutoff := now.Add(-24 * time.Hour)

	for _, w := range workers {
		if w.PoolID != poolID {
			continue
		}
		if scheduler.CountsTowardPoolConcurrency(w.State) {
			h.ActiveWorkers++
		}
		if w.State == core.WorkerBusy {
			h.BusyWorkers++
		}
		if w.RegisteredAt.After(h.LastRegisteredAt) {
			h.LastRegisteredAt = w.RegisteredAt
		}
		if w.StartedAt.After(h.LastJobStartedAt) {
			h.LastJobStartedAt = w.StartedAt
		}
		if w.CompletedAt.After(h.LastJobCompletedAt) {
			h.LastJobCompletedAt = w.CompletedAt
		}

		// GitHub's final job result is authoritative for runtime health. A worker
		// may record a later provider/cleanup failure after a successful job; that
		// is infrastructure cleanup evidence, not a failed repository workload.
		if !jobSucceeded(w.JobResult) {
			if w.FailedAt.After(h.LastFailureAt) {
				h.LastFailureAt = w.FailedAt
				h.LastFailureReason = w.FailureReason
			}
			if !w.FailedAt.IsZero() && !w.FailedAt.Before(cutoff) {
				h.RecentFailures++
			}
		}
	}

	if h.BusyWorkers > 0 {
		h.State = "active"
		return h
	}

	lastSuccess := h.LastJobCompletedAt
	if h.LastRegisteredAt.After(lastSuccess) {
		lastSuccess = h.LastRegisteredAt
	}
	if h.LastJobStartedAt.After(lastSuccess) {
		lastSuccess = h.LastJobStartedAt
	}

	if !h.LastFailureAt.IsZero() && (lastSuccess.IsZero() || h.LastFailureAt.After(lastSuccess)) {
		h.State = "degraded"
		return h
	}
	if !lastSuccess.IsZero() {
		h.State = "healthy"
	}
	return h
}
