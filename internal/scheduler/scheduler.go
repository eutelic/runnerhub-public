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
	PlacementDomains int            `json:"placementDomains"`
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
	var workers []core.Worker
	if live, ok := s.Store.(store.NonDeletedWorkerReader); ok {
		workers, err = live.ListNonDeletedWorkers(ctx)
	} else {
		workers, err = s.Store.ListWorkers(ctx)
	}
	if err != nil {
		return Decision{}, err
	}
	byID := make(map[string]core.Host, len(hosts))
	domainCapacity := make(map[string]core.Resources)
	for _, h := range hosts {
		byID[h.ID] = h
		if h.State != core.HostOnline { continue }
		key := placementDomainKey(h)
		if cap, ok := domainCapacity[key]; ok { domainCapacity[key] = minimumResources(cap, h.Capacity) } else { domainCapacity[key] = h.Capacity }
	}
	used := map[string]core.Resources{}
	poolActive := 0
	for _, w := range workers {
		if OccupiesHostResources(w) {
			key := PlacementDomainForWorker(byID, w.HostID)
			used[key] = core.Add(used[key], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			poolActive++
		}
	}
	if poolActive >= pool.MaxConcurrency {
		return Decision{}, fmt.Errorf("%w: pool concurrency exhausted (%d/%d)", ErrNoCapacity, poolActive, pool.MaxConcurrency)
	}
	type candidate struct {
		h        core.Host
		used     core.Resources
		priority int
		score    int64
	}
	var candidates []candidate
	var rejects []string
	for _, h := range hosts {
		key := placementDomainKey(h)
		cap := domainCapacity[key]
		if reason := hostRejectionReasonWithCapacity(h, pool, used[key], cap); reason != "" {
			rejects = append(rejects, h.Name+": "+reason)
			continue
		}
		u := used[key]
		req := EffectivePoolResources(h, pool)
		score := (cap.CPUMillis - u.CPUMillis - req.CPUMillis) +
			(cap.MemoryBytes-u.MemoryBytes-req.MemoryBytes)/(1024*1024)
		candidates = append(candidates, candidate{h: h, used: u, priority: h.Priority, score: score})
	}
	if len(candidates) == 0 {
		return Decision{}, fmt.Errorf("%w: %s", ErrNoCapacity, strings.Join(rejects, "; "))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority > candidates[j].priority
		}
		return candidates[i].score > candidates[j].score
	})
	c := candidates[0]
	return Decision{Host: c.h, Used: c.used, Reason: fmt.Sprintf("best compatible host: priority=%d, residual-capacity-score=%d", c.priority, c.score)}, nil
}

func SummarizeCapacity(pool core.RunnerPool, hosts []core.Host, workers []core.Worker) CapacitySummary {
	byID := make(map[string]core.Host, len(hosts))
	domainCapacity := make(map[string]core.Resources)
	for _, h := range hosts {
		byID[h.ID] = h
		if h.State != core.HostOnline { continue }
		key := placementDomainKey(h)
		if cap, ok := domainCapacity[key]; ok { domainCapacity[key] = minimumResources(cap, h.Capacity) } else { domainCapacity[key] = h.Capacity }
	}
	used := map[string]core.Resources{}
	summary := CapacitySummary{Requested: pool.Resources}
	for _, w := range workers {
		if OccupiesHostResources(w) {
			key := PlacementDomainForWorker(byID, w.HostID)
			used[key] = core.Add(used[key], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			summary.ActiveWorkers++
			if w.State == core.WorkerBusy {
				summary.BusyWorkers++
			}
		}
	}

	var capacityReasons []string
	type eligibleDomain struct { capacity, used core.Resources; slots int; reason string; initialized bool }
	domains := map[string]*eligibleDomain{}
	for _, h := range hosts {
		if h.State != core.HostOnline || !matchTags(h.Tags, pool.HostSelector) || !supportsIsolation(h, pool.Isolation) || specializedHostRejectionReason(h, pool) != "" {
			continue
		}
		key := placementDomainKey(h)
		summary.CompatibleHosts++
		d, ok := domains[key]
		if !ok { d = &eligibleDomain{capacity:domainCapacity[key], used:used[key]}; domains[key] = d }
		slots := resourceFitCount(d.capacity, d.used, EffectivePoolResources(h, pool))
		if !d.initialized || slots > d.slots { d.slots = slots; d.reason = resourceExhaustionReason(d.capacity,d.used,EffectivePoolResources(h,pool)); d.initialized = true }
	}
	for _, d := range domains { summary.PlacementDomains++; summary.Capacity = core.Add(summary.Capacity,d.capacity); summary.Reserved = core.Add(summary.Reserved,d.used); summary.AdditionalWorkers += d.slots; if d.slots == 0 && d.reason != "" { capacityReasons = append(capacityReasons,d.reason) } }
	summary.CapacityLimit = summary.ActiveWorkers + summary.AdditionalWorkers

	switch {
	case pool.MaxConcurrency > 0 && summary.ActiveWorkers >= pool.MaxConcurrency:
		summary.Bottleneck = "pool_concurrency"
		summary.Reason = fmt.Sprintf("pool concurrency exhausted (%d/%d)", summary.ActiveWorkers, pool.MaxConcurrency)
	case summary.CompatibleHosts == 0:
		summary.Bottleneck = "no_compatible_host"
		summary.Reason = "no compatible online hosts"
		if core.IsAndroidEmulatorPool(pool) {
			summary.Reason = "Android emulator requires an online Linux amd64 process host with verified /dev/kvm and dedicated android-emulator tag"
		}
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
	return hostRejectionReasonWithCapacity(h,pool,used,h.Capacity)
}

func HostRejectionReasonWithCapacity(h core.Host,pool core.RunnerPool,used,capacity core.Resources) string { return hostRejectionReasonWithCapacity(h,pool,used,capacity) }

func hostRejectionReasonWithCapacity(h core.Host, pool core.RunnerPool, used, capacity core.Resources) string {
	if h.State != core.HostOnline {
		return "not online"
	}
	if !matchTags(h.Tags, pool.HostSelector) {
		return "selector mismatch"
	}
	if reason := specializedHostRejectionReason(h, pool); reason != "" {
		return reason
	}
	if !supportsIsolation(h, pool.Isolation) {
		return "isolation unsupported"
	}
	if reason := resourceExhaustionReason(capacity, used, EffectivePoolResources(h, pool)); reason != "" {
		return reason
	}
	return ""
}

func PlacementDomainKey(h core.Host) string { if id:=strings.TrimSpace(h.PlacementDomainID); id!="" { return "domain:"+id }; return "host:"+h.ID }
func placementDomainKey(h core.Host) string { return PlacementDomainKey(h) }
func PlacementDomainForWorker(hosts map[string]core.Host, hostID string) string { seen:=map[string]bool{}; for hostID!=""&&!seen[hostID] { seen[hostID]=true; h,ok:=hosts[hostID]; if !ok { return "host:"+hostID }; if h.State==core.HostArchived&&h.MergedIntoHostID!="" { hostID=h.MergedIntoHostID; continue }; return placementDomainKey(h) }; return "host:"+hostID }
func minimumResources(a,b core.Resources) core.Resources { min:=func(x,y int64)int64{if x<y{return x};return y}; gpus:=a.GPUs;if b.GPUs<gpus{gpus=b.GPUs};return core.Resources{CPUMillis:min(a.CPUMillis,b.CPUMillis),MemoryBytes:min(a.MemoryBytes,b.MemoryBytes),DiskBytes:min(a.DiskBytes,b.DiskBytes),GPUs:gpus} }

func specializedHostRejectionReason(h core.Host, pool core.RunnerPool) string {
  if h.SystemTags[core.AndroidEmulatorDedicatedTag]=="true" && !core.IsAndroidEmulatorPool(pool){
    return "dedicated native KVM host only accepts Android emulator jobs"
  }
  if !core.IsAndroidEmulatorPool(pool) {
    return ""
  }
  if h.Provider != "process" || h.OS != "linux" || h.Architecture != "amd64" {
    return "Android emulator requires a Linux amd64 native process host"
  }
  if h.SystemTags[core.AndroidEmulatorCapabilityTag] != "true" {
    return "KVM unavailable to the host runner user (verified hardware.kvm capability missing)"
  }
  return ""
}

func EffectiveWorkerResources(w core.Worker) core.Resources {
	r := w.Resources
	if tmpfsWorkdir(w.ProviderID, w.Isolation) {
		r.DiskBytes = 0
	}
	if dockerDinD(w.ProviderID, w.Isolation) && !w.AggregateResources {
		r = conservativeDinDReservation(r)
	}
	return r
}

func EffectivePoolResources(h core.Host, pool core.RunnerPool) core.Resources {
	r := pool.Resources
	if tmpfsWorkdir(h.Provider, pool.Isolation) {
		r.DiskBytes = 0
	}
	if dockerDinD(h.Provider, pool.Isolation) && !HostSupportsAggregateResourceGroups(h) {
		r = conservativeDinDReservation(r)
	}
	return r
}

func dockerDinD(provider string, isolation core.IsolationMode) bool {
	return provider == "docker" && isolation == core.IsolationContainerDinD
}

func HostSupportsAggregateResourceGroups(h core.Host) bool {
	return h.Provider == "docker" && h.Tags != nil && h.Tags["feature.aggregateResourceGroups"] == "true"
}

func conservativeDinDReservation(r core.Resources) core.Resources {
	// A DinD worker is two outer containers: the Actions runner and a privileged
	// Docker daemon. Both receive the requested per-container CPU/RAM ceiling,
	// so reserve two envelopes at the host scheduler. Disk is already backed by
	// the host and is reserved once; GPU assignment also remains singular.
	r.CPUMillis = saturatingDouble(r.CPUMillis)
	r.MemoryBytes = saturatingDouble(r.MemoryBytes)
	return r
}

func saturatingDouble(v int64) int64 {
	const maxInt64 = int64(9223372036854775807)
	if v > maxInt64/2 {
		return maxInt64
	}
	return v * 2
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
