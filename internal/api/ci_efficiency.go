package api

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
	"github.com/eutelic/runnerhub/internal/core"
)

type ciEfficiencyCoverage struct {
	ExecutionLedger bool   `json:"executionLedger"`
	WasteFindings   bool   `json:"wasteFindings"`
	Detail          string `json:"detail,omitempty"`
}

type ciEfficiencyQueueSummary struct {
	Samples    int     `json:"samples"`
	P50Seconds float64 `json:"p50Seconds"`
	P95Seconds float64 `json:"p95Seconds"`
}

type ciEfficiencyQueue struct {
	Overall       ciEfficiencyQueueSummary            `json:"overall"`
	ByRunnerClass map[string]ciEfficiencyQueueSummary `json:"byRunnerClass"`
}

type ciEfficiencyResponse struct {
	AnalysisVersion string               `json:"analysisVersion"`
	Repository      string               `json:"repository"`
	Window          string               `json:"window"`
	WindowStart     time.Time            `json:"windowStart"`
	WindowEnd       time.Time            `json:"windowEnd"`
	Jobs            int                  `json:"jobs"`
	Execution       ciaudit.ResourceTotals `json:"execution"`
	Queue           ciEfficiencyQueue    `json:"queue"`
	Coverage        ciEfficiencyCoverage `json:"coverage"`
}

func parseCIEfficiencyWindow(raw string) (string, time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "30d":
		return "30d", 30 * 24 * time.Hour, nil
	case "7d":
		return "7d", 7 * 24 * time.Hour, nil
	case "90d":
		return "90d", 90 * 24 * time.Hour, nil
	default:
		return "", 0, errors.New("window must be one of 7d, 30d, or 90d")
	}
}

func (s *Service) ciEfficiency(w http.ResponseWriter, r *http.Request) {
	windowName, window, err := parseCIEfficiencyWindow(r.URL.Query().Get("window"))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	owner := strings.TrimSpace(r.PathValue("owner"))
	repo := strings.TrimSpace(r.PathValue("repo"))
	if owner == "" || repo == "" {
		problem(w, http.StatusBadRequest, errors.New("repository owner and name are required"))
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	pools, err := s.Store.ListPools(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, summarizeCIEfficiencyAt(owner+"/"+repo, windowName, window, time.Now().UTC(), workers, pools))
}

func summarizeCIEfficiencyAt(repository, windowName string, window time.Duration, now time.Time, workers []core.Worker, pools []core.RunnerPool) ciEfficiencyResponse {
	start := now.Add(-window)
	poolByID := make(map[string]core.RunnerPool, len(pools))
	for _, pool := range pools {
		poolByID[pool.ID] = pool
	}

	jobs := make([]ciaudit.JobExecution, 0, len(workers))
	queueAll := make([]float64, 0, len(workers))
	queueByClass := map[string][]float64{}
	for _, worker := range workers {
		if !strings.EqualFold(strings.TrimSpace(worker.Repository), repository) {
			continue
		}
		if job, ok := ciaudit.ExecutionFromWorker(worker); ok && job.CompletedAt.After(start) && job.StartedAt.Before(now) {
			if job.StartedAt.Before(start) {
				job.StartedAt = start
			}
			if job.CompletedAt.After(now) {
				job.CompletedAt = now
			}
			if job.StartedAt.Before(job.CompletedAt) {
				jobs = append(jobs, job)
			}
		}
		if worker.QueuedAt.IsZero() || worker.StartedAt.IsZero() || !worker.StartedAt.After(worker.QueuedAt) {
			continue
		}
		if worker.StartedAt.Before(start) || worker.StartedAt.After(now) {
			continue
		}
		seconds := worker.StartedAt.Sub(worker.QueuedAt).Seconds()
		queueAll = append(queueAll, seconds)
		class := ciRunnerClass(poolByID[worker.PoolID], worker.PoolID)
		queueByClass[class] = append(queueByClass[class], seconds)
	}

	report := ciaudit.Summarize(jobs, nil)
	byClass := make(map[string]ciEfficiencyQueueSummary, len(queueByClass))
	for class, samples := range queueByClass {
		byClass[class] = summarizeQueueSamples(samples)
	}
	return ciEfficiencyResponse{
		AnalysisVersion: ciaudit.AnalyzerVersion,
		Repository:      repository,
		Window:          windowName,
		WindowStart:     start,
		WindowEnd:       now,
		Jobs:            len(jobs),
		Execution:       report.Total,
		Queue: ciEfficiencyQueue{
			Overall:       summarizeQueueSamples(queueAll),
			ByRunnerClass: byClass,
		},
		Coverage: ciEfficiencyCoverage{
			ExecutionLedger: true,
			WasteFindings:   false,
			Detail:          "execution and queue metrics are live; deterministic waste findings are not yet persisted/ingested by this endpoint",
		},
	}
}

func ciRunnerClass(pool core.RunnerPool, fallback string) string {
	if isPublisherPool(pool) {
		return publisherScaleSetName
	}
	if name := strings.TrimSpace(pool.ScaleSetName); name != "" {
		return name
	}
	for _, label := range pool.Labels {
		label = strings.TrimSpace(label)
		if strings.EqualFold(label, "runnerhub") || strings.HasPrefix(strings.ToLower(label), "runnerhub-") {
			return label
		}
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return "unknown"
}

func summarizeQueueSamples(samples []float64) ciEfficiencyQueueSummary {
	if len(samples) == 0 {
		return ciEfficiencyQueueSummary{}
	}
	values := append([]float64(nil), samples...)
	sort.Float64s(values)
	return ciEfficiencyQueueSummary{
		Samples:    len(values),
		P50Seconds: percentileNearestRank(values, 0.50),
		P95Seconds: percentileNearestRank(values, 0.95),
	}
}

func percentileNearestRank(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
