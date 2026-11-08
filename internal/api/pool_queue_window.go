package api

import (
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

// poolQueueEvidence is a bounded seven-day time window over persisted worker
// starts, not an estimate of current GitHub queue depth. Missing and invalid
// queue timestamps never become artificial zero-second samples.
type poolQueueEvidence struct {
	WindowStart     time.Time `json:"windowStart"`
	WindowEnd       time.Time `json:"windowEnd"`
	StartedJobs     int       `json:"startedJobs"`
	Samples         int       `json:"samples"`
	CoveragePercent float64   `json:"coveragePercent"`
	P50Seconds      *float64  `json:"p50Seconds"`
	P95Seconds      *float64  `json:"p95Seconds"`
}

func summarizeRecentPoolQueue(poolID string, workers []core.Worker, now time.Time) poolQueueEvidence {
	now = now.UTC()
	start := now.Add(-7 * 24 * time.Hour)
	result := poolQueueEvidence{WindowStart: start, WindowEnd: now}
	samples := make([]float64, 0)
	for _, worker := range workers {
		if worker.PoolID != poolID || worker.StartedAt.IsZero() ||
			worker.StartedAt.Before(start) || worker.StartedAt.After(now) {
			continue
		}
		result.StartedJobs++
		if worker.QueuedAt.IsZero() || worker.QueuedAt.After(worker.StartedAt) {
			continue
		}
		// A measured zero delay is legitimate. Timestamp absence and negative
		// time skew are excluded, not silently rounded down.
		samples = append(samples, worker.StartedAt.Sub(worker.QueuedAt).Seconds())
	}
	summary := summarizeQueueSamples(samples)
	result.Samples = summary.Samples
	result.CoveragePercent = percentOf(result.Samples, result.StartedJobs)
	result.P50Seconds = summary.P50Seconds
	result.P95Seconds = summary.P95Seconds
	return result
}
