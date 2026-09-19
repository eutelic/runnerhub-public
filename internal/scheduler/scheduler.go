package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

var ErrNoCapacity = errors.New("no compatible host capacity")

type Decision struct {
	Host   core.Host      `json:"host"`
	Reason string         `json:"reason"`
	Used   core.Resources `json:"used"`
}

type CapacitySummary struct {
	CompatibleHosts   int            `json:"compatibleHosts"`
	ActiveWorkers     int            `json:"activeWorkers"`
	BusyWorkers       int            `json:"busyWorkers"`
	Requested         core.Resources `json:"requested"`
	Capacity          core.Resources `json:"capacity"`
	Reserved          core.Resources `json:"reserved"`
	AdditionalWorkers int            `json:"additionalWorkers"`
	CapacityLimit     int            `json:"capacityLimit"`
	Bottleneck        string         `json:"bottleneck"`
	Reason            string         `json:"reason,omitempty"`
}

type Scheduler struct{ Store store.Store }

func (s Scheduler) SelectHost(ctx context.Context, pool core.RunnerPool) (Decision, error) {
	hosts, err := s.Store.ListHosts(ctx)
	if err != nil {
		return Decision{}, err
	}
	workers, err := s.Store.ListWorkers(ctx)
	if err != nil {
		return Decision{}, err
	}
	used := map[string]core.Resources{}
	poolActive := 0
	for _, w := range workers {
		if OccupiesHostResources(w) {
			used[w.HostID] = core.Add(used[w.HostID], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			poolActive++
		}
	}
	if poolActive >= pool.MaxConcurrency {
		return Decision{}, fmt.Errorf("%w: pool concurrency exhausted (%d/%d)", ErrNoCapacity, poolActive, pool.MaxConcurrency)
	}
	type candidate struct {
		h     core.Host
		used  core.Resources
		score int64
	}
	var candidates []candidate
	var rejects []string
	for _, h := range hosts {
		if reason := HostRejectionReason(h, pool, used[h.ID]); reason != "" {
			rejects = append(rejects, h.Name+": "+reason)
			continue
		}
		u := used[h.ID]
		score := (h.Capacity.CPUMillis - u.CPUMillis - pool.Resources.CPUMillis) +
			(h.Capacity.MemoryBytes-u.MemoryBytes-pool.Resources.MemoryBytes)/(1024*1024)
		candidates = append(candidates, candidate{h: h, used: u, score: score})
	}
	if len(candidates) == 0 {
		return Decision{}, fmt.Errorf("%w: %s", ErrNoCapacity, strings.Join(rejects, "; "))
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	c := candidates[0]
	return Decision{Host: c.h, Used: c.used, Reason: "best compatible online host with available requested capacity"}, nil
}

func SummarizeCapacity(pool core.RunnerPool, hosts []core.Host, workers []core.Worker) CapacitySummary {
	used := map[string]core.Resources{}
	summary := CapacitySummary{Requested: pool.Resources}
	for _, w := range workers {
		if OccupiesHostResources(w) {
			used[w.HostID] = core.Add(used[w.HostID], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			summary.ActiveWorkers++
			if w.State == core.WorkerBusy {
				summary.BusyWorkers++
			}
		}
	}

	var capacityReasons []string
	for _, h := range hosts {
		if h.State != core.HostOnline || !matchTags(h.Tags, pool.HostSelector) || !supportsIsolation(h, pool.Isolation) {
			continue
		}
		summary.CompatibleHosts++
		summary.Capacity = core.Add(summary.Capacity, h.Capacity)
		summary.Reserved = core.Add(summary.Reserved, used[h.ID])
		req := EffectivePoolResources(h, pool)
		slots := resourceFitCount(h.Capacity, used[h.ID], req)
		summary.AdditionalWorkers += slots
		if slots == 0 {
			if reason := resourceExhaustionReason(h.Capacity, used[h.ID], req); reason != "" {
				capacityReasons = append(capacityReasons, h.Name+": "+reason)
			}
		}
	}
	summary.CapacityLimit = summary.ActiveWorkers + summary.AdditionalWorkers

	switch {
	case pool.MaxConcurrency > 0 && summary.ActiveWorkers >= pool.MaxConcurrency:
		summary.Bottleneck = "pool_concurrency"
		summary.Reason = fmt.Sprintf("pool concurrency exhausted (%d/%d)", summary.ActiveWorkers, pool.MaxConcurrency)
	case summary.CompatibleHosts == 0:
		summary.Bottleneck = "no_compatible_host"
		summary.Reason = "no compatible online hosts"
	case summary.AdditionalWorkers == 0:
		summary.Bottleneck = "host_resources"
		summary.Reason = strings.Join(capacityReasons, "; ")
	case pool.MaxConcurrency > 0 && pool.MaxConcurrency-summary.ActiveWorkers <= summary.AdditionalWorkers:
		summary.Bottleneck = "pool_concurrency"
		summary.Reason = fmt.Sprintf("pool max concurrency %d is the next scaling limit", pool.MaxConcurrency)
	default:
		summary.Bottleneck = "host_resources"
		summary.Reason = fmt.Sprintf("compatible hosts can fit %d additional worker(s)", summary.AdditionalWorkers)
	}
	return summary
}

func resourceFitCount(capacity, used, requested core.Resources) int {
	slots := -1
	apply := func(available, request int64) {
		if request <= 0 {
			return
		}
		n := int(available / request)
		if n < 0 {
			n = 0
		}
		if slots < 0 || n < slots {
			slots = n
		}
	}
	apply(capacity.CPUMillis-used.CPUMillis, requested.CPUMillis)
	apply(capacity.MemoryBytes-used.MemoryBytes, requested.MemoryBytes)
	apply(capacity.DiskBytes-used.DiskBytes, requested.DiskBytes)
	apply(int64(capacity.GPUs-used.GPUs), int64(requested.GPUs))
	if slots < 0 {
		return 0
	}
	return slots
}

func CountsTowardPoolConcurrency(s core.WorkerState) bool {
	switch s {
	case core.WorkerRequested, core.WorkerProvisioning, core.WorkerStarting, core.WorkerRegistering, core.WorkerReady, core.WorkerBusy:
		return true
	default:
		return false
	}
}

func OccupiesHostResources(w core.Worker) bool {
	switch w.State {
	case core.WorkerDeleted:
		return false
	case core.WorkerFailed, core.WorkerCompleted, core.WorkerCleanupPending, core.WorkerDeleting:
		return strings.TrimSpace(w.ProviderHandle) != ""
	default:
		return true
	}
}

func HostRejectionReason(h core.Host, pool core.RunnerPool, used core.Resources) string {
	if h.State != core.HostOnline {
		return "not online"
	}
	if !matchTags(h.Tags, pool.HostSelector) {
		return "selector mismatch"
	}
	if !supportsIsolation(h, pool.Isolation) {
		return "isolation unsupported"
	}
	if reason := resourceExhaustionReason(h.Capacity, used, EffectivePoolResources(h, pool)); reason != "" {
		return reason
	}
	return ""
}

func EffectiveWorkerResources(w core.Worker) core.Resources {
	r := w.Resources
	if tmpfsWorkdir(w.ProviderID, w.Isolation) {
		r.DiskBytes = 0
	}
	return r
}

func EffectivePoolResources(h core.Host, pool core.RunnerPool) core.Resources {
	r := pool.Resources
	if tmpfsWorkdir(h.Provider, pool.Isolation) {
		r.DiskBytes = 0
	}
	return r
}

func tmpfsWorkdir(provider string, isolation core.IsolationMode) bool {
	return isolation == core.IsolationContainerTrusted && (provider == "docker" || provider == "truenas")
}

func resourceExhaustionReason(cap, used, req core.Resources) string {
	var reasons []string
	if available := cap.CPUMillis - used.CPUMillis; available < req.CPUMillis {
		reasons = append(reasons, fmt.Sprintf("cpu exhausted (available=%dm requested=%dm)", available, req.CPUMillis))
	}
	if available := cap.MemoryBytes - used.MemoryBytes; available < req.MemoryBytes {
		reasons = append(reasons, fmt.Sprintf("memory exhausted (available=%d requested=%d)", available, req.MemoryBytes))
	}
	if available := cap.DiskBytes - used.DiskBytes; available < req.DiskBytes {
		reasons = append(reasons, fmt.Sprintf("disk exhausted (available=%d requested=%d)", available, req.DiskBytes))
	}
	if available := cap.GPUs - used.GPUs; available < req.GPUs {
		reasons = append(reasons, fmt.Sprintf("gpu exhausted (available=%d requested=%d)", available, req.GPUs))
	}
	return strings.Join(reasons, ", ")
}

func matchTags(tags, sel map[string]string) bool {
	for k, v := range sel {
		if tags[k] != v {
			return false
		}
	}
	return true
}

func supportsIsolation(h core.Host, mode core.IsolationMode) bool {
	if h.Tags == nil {
		return mode == core.IsolationContainerTrusted && h.Provider == "docker"
	}
	key := "isolation." + string(mode)
	if v, ok := h.Tags[key]; ok {
		return v == "true"
	}
	return mode == core.IsolationContainerTrusted && h.Provider == "docker"
}
