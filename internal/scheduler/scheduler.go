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
	PlacementDomains  int            `json:"placementDomains"`
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
		if h.State != core.HostOnline || h.SchedulingDisabled {
			continue
		}
		key := placementDomainKey(h)
		if cap, ok := domainCapacity[key]; ok {
			domainCapacity[key] = minimumResources(cap, h.Capacity)
		} else {
			domainCapacity[key] = h.Capacity
		}
	}
	used := map[string]core.Resources{}
	poolActive := 0
	for _, w := range workers {
		if OccupiesHostResources(w) {
			key := PlacementDomainForWorker(byID, w.HostID)
			used[key] = addReservationsSaturating(used[key], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			poolActive++
		}
	}
	// Validate before checking concurrency so malformed requests are never
	// reported as transient capacity shortages (which callers may retry).
	if pool.Resources.CPUMillis == 0 && pool.Resources.MemoryBytes == 0 && pool.Resources.DiskBytes == 0 && pool.Resources.GPUs == 0 {
		return Decision{}, fmt.Errorf("invalid pool resources: no positive reservation")
	}
	if pool.Resources.CPUMillis < 0 || pool.Resources.MemoryBytes < 0 || pool.Resources.DiskBytes < 0 || pool.Resources.GPUs < 0 {
		return Decision{}, fmt.Errorf("invalid pool resources: negative reservation")
	}
	if pool.MaxConcurrency < 0 {
		return Decision{}, fmt.Errorf("invalid pool max concurrency: %d", pool.MaxConcurrency)
	}
	if pool.MaxConcurrency > 0 && poolActive >= pool.MaxConcurrency {
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
		// A feasible candidate can still have enormous residual capacity.
		// Subtract only after checking bounds, and saturate the combined score
		// rather than allowing a large host to wrap into a negative ranking.
		cpuLeft := nonnegativeHeadroom(cap.CPUMillis, u.CPUMillis)
		memLeft := nonnegativeHeadroom(cap.MemoryBytes, u.MemoryBytes)
		cpuScore := nonnegativeHeadroom(cpuLeft, req.CPUMillis)
		memScore := nonnegativeHeadroom(memLeft, req.MemoryBytes) / (1024 * 1024)
		const maxInt64 = int64(^uint64(0) >> 1)
		score := cpuScore
		if memScore > maxInt64-score {
			score = maxInt64
		} else {
			score += memScore
		}
		candidates = append(candidates, candidate{h: h, used: u, priority: h.Priority, score: score})
	}
	if len(candidates) == 0 {
		if len(rejects) == 0 {
			return Decision{}, fmt.Errorf("%w: no hosts registered", ErrNoCapacity)
		}
		sort.Strings(rejects)
		return Decision{}, fmt.Errorf("%w: %s", ErrNoCapacity, strings.Join(rejects, "; "))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority > candidates[j].priority
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		// Stable placement independent of store iteration order on equal scores.
		return candidates[i].h.ID < candidates[j].h.ID
	})
	c := candidates[0]
	return Decision{Host: c.h, Used: c.used, Reason: fmt.Sprintf("best compatible host: priority=%d, residual-capacity-score=%d", c.priority, c.score)}, nil
}

func SummarizeCapacity(pool core.RunnerPool, hosts []core.Host, workers []core.Worker) CapacitySummary {
	byID := make(map[string]core.Host, len(hosts))
	domainCapacity := make(map[string]core.Resources)
	for _, h := range hosts {
		byID[h.ID] = h
		if h.State != core.HostOnline || h.SchedulingDisabled {
			continue
		}
		key := placementDomainKey(h)
		if cap, ok := domainCapacity[key]; ok {
			domainCapacity[key] = minimumResources(cap, h.Capacity)
		} else {
			domainCapacity[key] = h.Capacity
		}
	}
	used := map[string]core.Resources{}
	summary := CapacitySummary{Requested: pool.Resources}
	for _, w := range workers {
		if OccupiesHostResources(w) {
			key := PlacementDomainForWorker(byID, w.HostID)
			used[key] = addReservationsSaturating(used[key], EffectiveWorkerResources(w))
		}
		if w.PoolID == pool.ID && CountsTowardPoolConcurrency(w.State) {
			summary.ActiveWorkers++
			if w.State == core.WorkerBusy {
				summary.BusyWorkers++
			}
		}
	}

	// Invalid reservations must not produce misleading schedulable capacity.
	// In particular, negative requirements can inflate resourceFitCount.
	if pool.Resources.CPUMillis == 0 && pool.Resources.MemoryBytes == 0 && pool.Resources.DiskBytes == 0 && pool.Resources.GPUs == 0 {
		summary.Bottleneck = "invalid_pool_resources"
		summary.Reason = "invalid pool resources: no positive reservation"
		summary.CapacityLimit = summary.ActiveWorkers
		return summary
	}
	if pool.Resources.CPUMillis < 0 || pool.Resources.MemoryBytes < 0 || pool.Resources.DiskBytes < 0 || pool.Resources.GPUs < 0 {
		summary.Bottleneck = "invalid_pool_resources"
		summary.Reason = "invalid pool resources: negative reservation"
		summary.CapacityLimit = summary.ActiveWorkers
		return summary
	}

	if pool.MaxConcurrency < 0 {
		summary.Bottleneck = "invalid_pool_concurrency"
		summary.Reason = fmt.Sprintf("invalid pool max concurrency: %d", pool.MaxConcurrency)
		summary.CapacityLimit = summary.ActiveWorkers
		return summary
	}

	var capacityReasons []string
	type eligibleDomain struct {
		capacity, used core.Resources
		slots          int
		reason         string
		initialized    bool
	}
	domains := map[string]*eligibleDomain{}
	for _, h := range hosts {
		if h.State != core.HostOnline || h.SchedulingDisabled || !matchTags(h.Tags, pool.HostSelector) || !supportsIsolation(h, pool.Isolation) || specializedHostRejectionReason(h, pool) != "" {
			continue
		}
		key := placementDomainKey(h)
		summary.CompatibleHosts++
		d, ok := domains[key]
		if !ok {
			d = &eligibleDomain{capacity: domainCapacity[key], used: used[key]}
			domains[key] = d
		}
		// Mirror SelectHost: a disk-only pool on a trusted tmpfs host has
		// no effective reservation and must not advertise infinite slots.
		req := EffectivePoolResources(h, pool)
		if req.CPUMillis == 0 && req.MemoryBytes == 0 && req.DiskBytes == 0 && req.GPUs == 0 {
			continue
		}
		slots := resourceFitCount(d.capacity, d.used, req)
		if !d.initialized || slots > d.slots {
			d.slots = slots
			d.reason = resourceExhaustionReason(d.capacity, d.used, EffectivePoolResources(h, pool))
			d.initialized = true
		}
	}
	for _, d := range domains {
		summary.PlacementDomains++
		// Domain totals are diagnostic, but must not wrap into negative values
		// when multiple large hosts or reservations are aggregated.
		summary.Capacity = addReservationsSaturating(summary.Capacity, d.capacity)
		summary.Reserved = addReservationsSaturating(summary.Reserved, d.used)
		// Saturate aggregate slots: multiple very large placement domains must
		// never wrap the admission limit negative or below active workers.
		maxInt := int(^uint(0) >> 1)
		if d.slots > maxInt-summary.AdditionalWorkers {
			summary.AdditionalWorkers = maxInt
		} else {
			summary.AdditionalWorkers += d.slots
		}
		if d.slots == 0 && d.reason != "" {
			capacityReasons = append(capacityReasons, d.reason)
		}
	}
	sort.Strings(capacityReasons)
	// AdditionalWorkers is admission capacity, not raw physical slots.
	// A full pool must advertise zero new workers even when hosts have room.
	if pool.MaxConcurrency > 0 {
		remaining := pool.MaxConcurrency - summary.ActiveWorkers
		if remaining < 0 {
			remaining = 0
		}
		if summary.AdditionalWorkers > remaining {
			summary.AdditionalWorkers = remaining
		}
	}
	// Preserve the active count and saturate the advertised limit when
	// aggregate physical slots exceed the platform integer range.
	maxInt := int(^uint(0) >> 1)
	if summary.AdditionalWorkers > maxInt-summary.ActiveWorkers {
		summary.AdditionalWorkers = maxInt - summary.ActiveWorkers
	}
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

// addReservationsSaturating prevents aggregate worker reservations from wrapping
// into apparent free capacity. Invalid negative accounting fails closed.
func addReservationsSaturating(a, b core.Resources) core.Resources {
	const maxInt64 = int64(^uint64(0) >> 1)
	maxInt := int(^uint(0) >> 1)
	add64 := func(x, y int64) int64 {
		if x < 0 || y < 0 || x > maxInt64-y {
			return maxInt64
		}
		return x + y
	}
	addInt := func(x, y int) int {
		if x < 0 || y < 0 || x > maxInt-y {
			return maxInt
		}
		return x + y
	}
	return core.Resources{
		CPUMillis: add64(a.CPUMillis, b.CPUMillis),
		MemoryBytes: add64(a.MemoryBytes, b.MemoryBytes),
		DiskBytes: add64(a.DiskBytes, b.DiskBytes),
		GPUs: addInt(a.GPUs, b.GPUs),
	}
}

// nonnegativeHeadroom treats corrupt reservations as exhausted and avoids overflow.
func nonnegativeHeadroom(total, reserved int64) int64 {
	if total <= 0 || reserved < 0 || reserved >= total {
		return 0
	}
	return total - reserved
}

func resourceFitCount(capacity, used, requested core.Resources) int {
	slots := -1
	apply := func(available, request int64) {
		if request <= 0 {
			return
		}
		// Clamp before converting to int: an unusually large host capacity
		// must not wrap into a negative slot count and hide usable capacity.
		n64 := available / request
		if n64 < 0 {
			n64 = 0
		}
		maxInt := int64(^uint(0) >> 1)
		if n64 > maxInt {
			n64 = maxInt
		}
		n := int(n64)
		if slots < 0 || n < slots {
			slots = n
		}
	}
	// Compute nonnegative headroom without subtracting first. A malformed or
	// over-reserved host can have used > capacity; direct int64 subtraction
	// may overflow and incorrectly advertise available worker slots.
	remaining := func(total, reserved int64) int64 {
		if total <= 0 || reserved >= total {
			return 0
		}
		if reserved < 0 {
			// Corrupt accounting cannot establish any safe headroom.
			return 0
		}
		return total - reserved
	}
	apply(remaining(capacity.CPUMillis, used.CPUMillis), requested.CPUMillis)
	apply(remaining(capacity.MemoryBytes, used.MemoryBytes), requested.MemoryBytes)
	apply(remaining(capacity.DiskBytes, used.DiskBytes), requested.DiskBytes)
	apply(remaining(int64(capacity.GPUs), int64(used.GPUs)), int64(requested.GPUs))
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
	return hostRejectionReasonWithCapacity(h, pool, used, h.Capacity)
}

func HostRejectionReasonWithCapacity(h core.Host, pool core.RunnerPool, used, capacity core.Resources) string {
	return hostRejectionReasonWithCapacity(h, pool, used, capacity)
}

func hostRejectionReasonWithCapacity(h core.Host, pool core.RunnerPool, used, capacity core.Resources) string {
	if h.SchedulingDisabled {
		return "host disabled for scheduling"
	}
	// A disk-only reservation becomes zero on trusted tmpfs workers. Such a
	// worker would consume no accounted resources and permit unbounded admission.
	if r := EffectivePoolResources(h, pool); r.CPUMillis == 0 && r.MemoryBytes == 0 && r.DiskBytes == 0 && r.GPUs == 0 {
		return "effective pool reservation is zero on this host"
	}
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

func PlacementDomainKey(h core.Host) string {
	if id := strings.TrimSpace(h.PlacementDomainID); id != "" {
		return "domain:" + id
	}
	return "host:" + h.ID
}
func placementDomainKey(h core.Host) string { return PlacementDomainKey(h) }
func PlacementDomainForWorker(hosts map[string]core.Host, hostID string) string {
	seen := map[string]bool{}
	for hostID != "" && !seen[hostID] {
		seen[hostID] = true
		h, ok := hosts[hostID]
		if !ok {
			return "host:" + hostID
		}
		if h.State == core.HostArchived && h.MergedIntoHostID != "" {
			hostID = h.MergedIntoHostID
			continue
		}
		return placementDomainKey(h)
	}
	return "host:" + hostID
}
func minimumResources(a, b core.Resources) core.Resources {
	min := func(x, y int64) int64 {
		if x < y {
			return x
		}
		return y
	}
	gpus := a.GPUs
	if b.GPUs < gpus {
		gpus = b.GPUs
	}
	return core.Resources{CPUMillis: min(a.CPUMillis, b.CPUMillis), MemoryBytes: min(a.MemoryBytes, b.MemoryBytes), DiskBytes: min(a.DiskBytes, b.DiskBytes), GPUs: gpus}
}

func specializedHostRejectionReason(h core.Host, pool core.RunnerPool) string {
	if h.SystemTags[core.AndroidEmulatorDedicatedTag] == "true" && !core.IsAndroidEmulatorPool(pool) {
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
	// Check reservations before subtraction: over-reserved or malformed
	// accounting must not overflow into apparent free host capacity.
	remaining := func(total, reserved int64) int64 {
		if total <= 0 || reserved >= total {
			return 0
		}
		if reserved < 0 {
			return 0
		}
		return total - reserved
	}
	if available := remaining(cap.CPUMillis, used.CPUMillis); available < req.CPUMillis {
		reasons = append(reasons, fmt.Sprintf("cpu exhausted (available=%dm requested=%dm)", available, req.CPUMillis))
	}
	if available := remaining(cap.MemoryBytes, used.MemoryBytes); available < req.MemoryBytes {
		reasons = append(reasons, fmt.Sprintf("memory exhausted (available=%d requested=%d)", available, req.MemoryBytes))
	}
	if available := remaining(cap.DiskBytes, used.DiskBytes); available < req.DiskBytes {
		reasons = append(reasons, fmt.Sprintf("disk exhausted (available=%d requested=%d)", available, req.DiskBytes))
	}
	if available := remaining(int64(cap.GPUs), int64(used.GPUs)); available < int64(req.GPUs) {
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
