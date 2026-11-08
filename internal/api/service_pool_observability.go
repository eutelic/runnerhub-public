package api

import (
	"errors"
	cachepkg "github.com/eutelic/runnerhub/internal/cache"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/fleet"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
	"github.com/eutelic/runnerhub/internal/telemetry"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Service) poolHealth(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	var workers []core.Worker
	if live, ok := s.Store.(store.NonDeletedWorkerReader); ok {
		workers, err = live.ListNonDeletedWorkers(r.Context())
	} else {
		workers, err = s.Store.ListWorkers(r.Context())
	}
	if err != nil {
		problem(w, 500, err)
		return
	}
	health := fleet.PoolHealth(p, hosts, workers, time.Now().UTC())
	maintenance, err := s.maintenanceState(r.Context())
	if err != nil {
		problem(w, 503, err)
		return
	}
	if maintenance.Paused {
		health.Healthy = false
		health.Reasons = append(health.Reasons,
			"RunnerHub maintenance: new CI admissions paused for self-redeployment")
		// Keep currently admitted jobs visible; do not advertise spare
		// physical slots during the global maintenance gate.
		health.CapacityLimit = min(health.CapacityLimit, health.ActiveWorkers)
	}
	writeJSON(w, 200, health)
}
func (s *Service) runtimeHealth(w http.ResponseWriter, r *http.Request) {
	pools, err := s.Store.ListPools(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	writeJSON(w, 200, fleet.RuntimeHealthByPool(pools, workers, time.Now().UTC()))
}

type poolLatencySummary struct {
	Queue               string            `json:"queue"`
	Provision           string            `json:"provision"`
	Registration        string            `json:"registration"`
	Execution           string            `json:"execution"`
	Cleanup             string            `json:"cleanup"`
	QueueSamples        uint64            `json:"queueSamples"`
	ProvisionSamples    int               `json:"provisionSamples"`
	RegistrationSamples int               `json:"registrationSamples"`
	ExecutionSamples    int               `json:"executionSamples"`
	CleanupSamples      int               `json:"cleanupSamples"`
	RecentQueue         poolQueueEvidence `json:"recentQueue"`
}
type durationAccumulator struct {
	total   time.Duration
	samples int
}

func (d *durationAccumulator) add(start, end time.Time) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return
	}
	d.total += end.Sub(start)
	d.samples++
}
func (d durationAccumulator) average() time.Duration {
	if d.samples == 0 {
		return 0
	}
	return d.total / time.Duration(d.samples)
}
func boundedSampleCount(v uint64) (int64, bool) {
	if v > uint64((1<<63)-1) {
		return 0, false
	}
	return int64(v), true
} // #nosec G115 -- conversion is guarded by an explicit MaxInt64 bound
func averageDuration(total time.Duration, samples uint64) time.Duration {
	n, ok := boundedSampleCount(samples)
	if !ok || n == 0 {
		return 0
	}
	return total / time.Duration(n)
}
func rollingAverage(current, sample int64, samples uint64) int64 {
	n, ok := boundedSampleCount(samples)
	if !ok || n >= int64((1<<63)-1) {
		return current
	}
	return current + (sample-current)/(n+1)
}
func summarizePoolLatencies(poolID string, workers []core.Worker, metrics *telemetry.Metrics) poolLatencySummary {
	var provision, registration, execution, cleanup durationAccumulator
	for _, wk := range workers {
		if wk.PoolID != poolID {
			continue
		}
		provision.add(wk.CreatedAt, wk.RegistrationStartedAt)
		registration.add(wk.RegistrationStartedAt, wk.RegisteredAt)
		execution.add(wk.StartedAt, wk.CompletedAt)
		terminal := wk.CompletedAt
		if terminal.IsZero() {
			terminal = wk.FailedAt
		}
		if wk.State == core.WorkerDeleted {
			cleanup.add(terminal, wk.UpdatedAt)
		}
	}
	queue := telemetry.PoolSnapshot{}
	if metrics != nil {
		queue = metrics.PoolSnapshot(poolID)
	}
	queueAvg := time.Duration(0)
	if queue.QueueSamples > 0 {
		queueAvg = averageDuration(queue.QueueTotal, queue.QueueSamples)
	}
	return poolLatencySummary{
		Queue: queueAvg.String(), Provision: provision.average().String(), Registration: registration.average().String(),
		Execution: execution.average().String(), Cleanup: cleanup.average().String(), QueueSamples: queue.QueueSamples,
		ProvisionSamples: provision.samples, RegistrationSamples: registration.samples, ExecutionSamples: execution.samples, CleanupSamples: cleanup.samples,
		RecentQueue: summarizeRecentPoolQueue(poolID, workers, time.Now().UTC()),
	}
}
func (s *Service) poolRecommendation(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	capacity := scheduler.SummarizeCapacity(p, hosts, workers)
	util := 0.0
	if capacity.ActiveWorkers > 0 {
		util = float64(capacity.BusyWorkers) / float64(capacity.ActiveWorkers)
	}
	latencies := summarizePoolLatencies(p.ID, workers, s.Metrics)
	queueSnap := telemetry.PoolSnapshot{}
	if s.Metrics != nil {
		queueSnap = s.Metrics.PoolSnapshot(p.ID)
	}
	avgQueue := time.Duration(0)
	if queueSnap.QueueSamples > 0 {
		avgQueue = averageDuration(queueSnap.QueueTotal, queueSnap.QueueSamples)
	}
	recommended := telemetry.RecommendConcurrency(avgQueue, util, p.MaxConcurrency)
	if recommended > capacity.CapacityLimit {
		recommended = capacity.CapacityLimit
	}
	if recommended < capacity.ActiveWorkers {
		recommended = capacity.ActiveWorkers
	}
	writeJSON(w, 200, map[string]any{
		"current": p.MaxConcurrency, "recommended": recommended, "averageQueue": avgQueue.String(), "utilization": util,
		"latencies": latencies, "capacity": capacity, "resourceSizing": telemetry.RecommendResources(p, workers), "autoTuneResources": p.AutoTuneResources,
	})
}
func (s *Service) poolSecurity(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	posture, err := s.inspectSecurity(r.Context(), p)
	if err != nil {
		problem(w, 502, err)
		return
	}
	writeJSON(w, 200, posture)
}
func (s *Service) versions(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	pools, _ := s.Store.ListPools(r.Context())
	writeJSON(w, 200, map[string]any{"fleet": fleet.Versions(hosts, workers), "images": images.Inventory(pools, workers)})
}
func (s *Service) costEstimate(w http.ResponseWriter, r *http.Request) {
	var in telemetry.CostInput
	if !decode(w, r, &in) {
		return
	}
	writeJSON(w, 200, telemetry.EstimateCost(in))
}
func (s *Service) cachePut(w http.ResponseWriter, r *http.Request) {
	if s.Cache == nil {
		problem(w, 503, errors.New("cache is not configured"))
		return
	}
	key, n, err := s.Cache.Put(r.Body)
	if errors.Is(err, cachepkg.ErrTooLarge) {
		problem(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	if err != nil {
		problem(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"key": key, "bytes": n})
}
func (s *Service) cacheGet(w http.ResponseWriter, r *http.Request) {
	if s.Cache == nil {
		problem(w, 503, errors.New("cache is not configured"))
		return
	}
	f, err := s.Cache.Open(r.PathValue("key"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, f)
}
func (s *Service) cacheDelete(w http.ResponseWriter, r *http.Request) {
	if s.Cache == nil {
		problem(w, 503, errors.New("cache is not configured"))
		return
	}
	if err := s.Cache.Remove(r.PathValue("key")); err != nil {
		problem(w, 400, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Service) pools(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListPools(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Service) createPool(w http.ResponseWriter, r *http.Request) {
	var p core.RunnerPool
	if !decode(w, r, &p) {
		return
	}
	if p.ID == "" {
		p.ID = "pool-" + randomID()
	}
	if err := core.ValidateRunnerPool(p); err != nil {
		problem(w, 400, err)
		return
	}
	if err := s.Policy.ValidatePool(p); err != nil {
		problem(w, 403, err)
		return
	}
	if s.Catalog != nil {
		posture, err := githubrunner.InspectPoolSecurity(r.Context(), s.Catalog, p)
		if err != nil {
			problem(w, 502, err)
			return
		}
		if !posture.Safe {
			problem(w, 409, errors.New(strings.Join(posture.Reasons, "; ")))
			return
		}
	}
	if err := s.Store.UpsertPool(r.Context(), p); err != nil {
		problem(w, 500, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "create_pool", ResourceType: "pool", ResourceID: p.ID, Result: "ok"})
	writeJSON(w, 201, p)
}
