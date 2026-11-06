package telemetry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

const maxTrackedPools = 256

type Snapshot struct {
	RunnerRequests   uint64        `json:"runnerRequests"`
	LifecycleSuccess uint64        `json:"lifecycleSuccess"`
	LifecycleFailure uint64        `json:"lifecycleFailure"`
	ProvisionTotal   time.Duration `json:"provisionTotal"`
	CleanupTotal     time.Duration `json:"cleanupTotal"`
	QueueSamples     uint64        `json:"queueSamples"`
	QueueTotal       time.Duration `json:"queueTotal"`
}

type PoolSnapshot struct {
	QueueSamples uint64        `json:"queueSamples"`
	QueueTotal   time.Duration `json:"queueTotal"`
}

type Metrics struct {
	mu        sync.Mutex
	s         Snapshot
	pools     map[string]PoolSnapshot
	poolOrder []string
}

func (m *Metrics) RunnerRequested() {
	m.mu.Lock()
	m.s.RunnerRequests++
	m.mu.Unlock()
}

func (m *Metrics) Lifecycle(ok bool, provision, cleanup time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.s.LifecycleSuccess++
	} else {
		m.s.LifecycleFailure++
	}
	m.s.ProvisionTotal += provision
	m.s.CleanupTotal += cleanup
}

func (m *Metrics) Queue(poolID string, d time.Duration) {
	// A measured zero wait is valid; only negative durations indicate clock skew.
	if d < 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.s.QueueSamples++
	m.s.QueueTotal += d
	if poolID == "" {
		return
	}
	if m.pools == nil {
		m.pools = make(map[string]PoolSnapshot)
	}
	if _, ok := m.pools[poolID]; !ok {
		if len(m.pools) >= maxTrackedPools {
			oldest := m.poolOrder[0]
			delete(m.pools, oldest)
			m.poolOrder = m.poolOrder[1:]
		}
		m.poolOrder = append(m.poolOrder, poolID)
	}
	s := m.pools[poolID]
	s.QueueSamples++
	s.QueueTotal += d
	m.pools[poolID] = s
}

func (m *Metrics) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s
}

func (m *Metrics) PoolSnapshot(poolID string) PoolSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pools[poolID]
}

type CostInput struct {
	Watts             float64
	Hours             float64
	EnergyPricePerKWh float64
	HardwareMonthly   float64
	Jobs               int
}

type CostEstimate struct {
	EnergyCost float64 `json:"energyCost"`
	TotalCost  float64 `json:"totalCost"`
	CostPerJob float64 `json:"costPerJob"`
}

func EstimateCost(i CostInput) CostEstimate {
	energy := i.Watts * i.Hours / 1000 * i.EnergyPricePerKWh
	total := energy + i.HardwareMonthly
	per := 0.0
	if i.Jobs > 0 {
		per = total / float64(i.Jobs)
	}
	return CostEstimate{EnergyCost: energy, TotalCost: total, CostPerJob: per}
}

func RecommendConcurrency(avgQueue time.Duration, utilization float64, current int) int {
	if current < 1 {
		current = 1
	}
	if avgQueue > 30*time.Second && utilization > 0.75 {
		return current + 1
	}
	if avgQueue < 5*time.Second && utilization < 0.25 && current > 1 {
		return current - 1
	}
	return current
}


const (
	resourceHistoryJobs = 50
	resourceMinimumJobs = 5
	resourceMinimumWellSampledJobs = 5
	resourceHistoryMaxAge = 90 * 24 * time.Hour
	cpuStepMillis int64 = 250
	memoryStepBytes int64 = 512 << 20
	minimumCPUMillis int64 = 500
	minimumMemoryBytes int64 = 1 << 30
)

type ResourceRecommendation struct {
	Jobs int `json:"jobs"`
	WellSampledJobs int `json:"wellSampledJobs,omitempty"`
	RejectedJobs int `json:"rejectedJobs,omitempty"`
	StaleJobs int `json:"staleJobs,omitempty"`
	Ready bool `json:"ready"`
	Confidence string `json:"confidence"`
	Current core.Resources `json:"current"`
	Recommended core.Resources `json:"recommended"`
	CPUPeakP95Millis int64 `json:"cpuPeakP95Millis,omitempty"`
	CPUPeakMaxMillis int64 `json:"cpuPeakMaxMillis,omitempty"`
	MemoryPeakP95Bytes int64 `json:"memoryPeakP95Bytes,omitempty"`
	MemoryPeakMaxBytes int64 `json:"memoryPeakMaxBytes,omitempty"`
	ResetAt *time.Time `json:"resetAt,omitempty"`
	UsageBasis string `json:"usageBasis"`
	Reason string `json:"reason"`
}

type usagePoint struct {
	at time.Time
	cpu int64
	memory int64
	samples uint64
	oom bool
}

func workerTerminalAt(worker core.Worker) time.Time {
	at := worker.CompletedAt
	if at.IsZero() {
		at = worker.FailedAt
	}
	if at.IsZero() {
		at = worker.UpdatedAt
	}
	return at
}

func workerUsageForSizing(pool core.RunnerPool, worker core.Worker) (int64, int64, bool) {
	cpu := worker.Usage.PeakCPUMillis
	memory := worker.Usage.PeakMemoryBytes
	isolation := worker.Isolation
	if isolation == "" {
		isolation = pool.Isolation
	}
	if isolation == core.IsolationContainerDinD {
		// The DinD resource contract is now a total logical-worker budget.
		// Historical/fallback jobs used independent per-container envelopes and
		// cannot be converted safely, so never mix them into aggregate learning.
		if !worker.AggregateResources || !worker.Usage.AggregateResources {
			return 0, 0, false
		}
		if cpu <= 0 || memory <= 0 {
			return 0, 0, false
		}
	} else {
		// For single-container workers the envelope is equivalent to aggregate
		// usage. Prefer it when available, while retaining compatibility with
		// historical trusted-runner samples.
		if worker.Usage.PeakEnvelopeCPUMillis > 0 {
			cpu = worker.Usage.PeakEnvelopeCPUMillis
		}
		if worker.Usage.PeakEnvelopeMemoryBytes > 0 {
			memory = worker.Usage.PeakEnvelopeMemoryBytes
		}
	}
	if worker.ProviderOOMKilled && worker.Resources.MemoryBytes > 0 && memory < worker.Resources.MemoryBytes {
		// An OOM kill is direct evidence that the configured memory ceiling was
		// insufficient even when the 15s sampling cadence missed the final spike.
		memory = worker.Resources.MemoryBytes
	}
	if cpu <= 0 || memory <= 0 {
		return 0, 0, false
	}

	// Reject impossible/corrupt samples instead of allowing one historical
	// telemetry bug to permanently inflate a repository's learned size.
	if worker.Resources.CPUMillis > 0 {
		tolerance := max64(cpuStepMillis, worker.Resources.CPUMillis/20)
		if cpu > worker.Resources.CPUMillis+tolerance {
			return 0, 0, false
		}
	}
	if worker.Resources.MemoryBytes > 0 {
		tolerance := max64(memoryStepBytes, worker.Resources.MemoryBytes/20)
		if memory > worker.Resources.MemoryBytes+tolerance {
			return 0, 0, false
		}
	}
	return cpu, memory, true
}

func RecommendResources(pool core.RunnerPool, workers []core.Worker) ResourceRecommendation {
	usageBasis := "per-container-envelope"
	if pool.Isolation == core.IsolationContainerDinD {
		usageBasis = "logical-worker-aggregate"
	}
	out := ResourceRecommendation{
		Current: pool.Resources,
		Recommended: pool.Resources,
		UsageBasis: usageBasis,
		Reason: "collecting completed-job usage samples",
	}
	if !pool.LearningResetAt.IsZero() {
		reset := pool.LearningResetAt
		out.ResetAt = &reset
	}
	points := make([]usagePoint, 0, len(workers))
	now := time.Now().UTC()
	for _, worker := range workers {
		if worker.PoolID != pool.ID || worker.Usage.Samples == 0 || worker.StartedAt.IsZero() {
			continue
		}
		terminal := !worker.CompletedAt.IsZero() || !worker.FailedAt.IsZero()
		switch worker.State {
		case core.WorkerCompleted, core.WorkerFailed, core.WorkerCleanupPending, core.WorkerDeleting, core.WorkerDeleted:
			terminal = true
		}
		if !terminal {
			continue
		}
		result := strings.ToLower(strings.TrimSpace(worker.JobResult))
		successful := result == "success" || result == "succeeded" || result == "completed" ||
			(result == "" && worker.FailedAt.IsZero() && !worker.CompletedAt.IsZero())
		if !successful && !worker.ProviderOOMKilled {
			continue
		}
		at := workerTerminalAt(worker)
		if !pool.LearningResetAt.IsZero() {
			// A reset begins a new cohort of workers, not merely a new completion
			// window. A runner created under the old limits may finish after the
			// reset and must not contaminate learning for the new configuration.
			cohortAt := worker.CreatedAt
			if cohortAt.IsZero() {
				cohortAt = worker.StartedAt
			}
			if cohortAt.IsZero() || !cohortAt.After(pool.LearningResetAt) {
				continue
			}
		}
		observedAt := worker.Usage.UpdatedAt
		if observedAt.IsZero() {
			observedAt = workerTerminalAt(worker)
		}
		if observedAt.IsZero() || now.Sub(observedAt) > resourceHistoryMaxAge {
			out.StaleJobs++
			continue
		}
		cpu, memory, valid := workerUsageForSizing(pool, worker)
		if !valid {
			out.RejectedJobs++
			continue
		}
		points = append(points, usagePoint{at: at, cpu: cpu, memory: memory, samples: worker.Usage.Samples, oom: worker.ProviderOOMKilled})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].at.After(points[j].at) })
	if len(points) > resourceHistoryJobs {
		points = points[:resourceHistoryJobs]
	}
	out.Jobs = len(points)
	for _, point := range points {
		if point.samples >= 2 {
			out.WellSampledJobs++
		}
	}
	if out.Jobs < resourceMinimumJobs || out.WellSampledJobs < resourceMinimumWellSampledJobs {
		out.Confidence = "collecting"
		out.Reason = fmt.Sprintf(
			"collecting well-sampled completed-job usage history; %d valid jobs and %d with at least 2 usage samples (need %d)",
			out.Jobs, out.WellSampledJobs, resourceMinimumWellSampledJobs,
		)
		if out.RejectedJobs > 0 {
			out.Reason += fmt.Sprintf("; %d incompatible or legacy samples ignored", out.RejectedJobs)
		}
		if out.StaleJobs > 0 {
			out.Reason += fmt.Sprintf("; %d stale telemetry samples ignored (older than 90 days)", out.StaleJobs)
		}
		return out
	}
	switch {
	case out.Jobs >= 25:
		out.Confidence = "high"
	case out.Jobs >= 10:
		out.Confidence = "medium"
	default:
		out.Confidence = "low"
	}
	cpus := make([]int64, 0, len(points))
	memories := make([]int64, 0, len(points))
	oomMemoryFloor := int64(0)
	for _, point := range points {
		cpus = append(cpus, point.cpu)
		memories = append(memories, point.memory)
		if point.oom && point.memory > oomMemoryFloor {
			oomMemoryFloor = point.memory
		}
	}
	out.CPUPeakP95Millis = percentile95(cpus)
	for _, cpu := range cpus {
		if cpu > out.CPUPeakMaxMillis {
			out.CPUPeakMaxMillis = cpu
		}
	}
	out.MemoryPeakP95Bytes = percentile95(memories)
	for _, memory := range memories {
		if memory > out.MemoryPeakMaxBytes {
			out.MemoryPeakMaxBytes = memory
		}
	}
	cpuTarget := roundUp(max64(minimumCPUMillis, addHeadroom(out.CPUPeakP95Millis)), cpuStepMillis)
	// Memory is a hard failure boundary, unlike CPU quota. A rare legitimate
	// memory-heavy job must not be discarded by p95 and then OOM after a
	// downsize, so memory sizing uses the maximum recent valid peak.
	memoryBasis := max64(out.MemoryPeakMaxBytes, oomMemoryFloor)
	memoryTarget := roundUp(max64(minimumMemoryBytes, addHeadroom(memoryBasis)), memoryStepBytes)
	if closeEnough(cpuTarget, pool.Resources.CPUMillis, cpuStepMillis) {
		cpuTarget = pool.Resources.CPUMillis
	}
	if closeEnough(memoryTarget, pool.Resources.MemoryBytes, memoryStepBytes) {
		memoryTarget = pool.Resources.MemoryBytes
	}
	// Upsizing can act on sparse samples because it only adds capacity.
	// Downsizing is riskier: require 25 jobs with at least two runtime samples
	// each so a collection of very short/sparsely observed jobs cannot shrink a
	// pool based on systematically missed bursts.
	canDownsize := out.Jobs >= 25 && out.WellSampledJobs >= 25
	if !canDownsize && cpuTarget < pool.Resources.CPUMillis {
		cpuTarget = pool.Resources.CPUMillis
	}
	cpuP95Target := cpuTarget
	cpuTailHeldDownsize := canDownsize && cpuP95Target < pool.Resources.CPUMillis && out.CPUPeakMaxMillis > cpuP95Target
	if cpuTailHeldDownsize {
		// Keep the current reservation while a recent, valid high-CPU job remains
		// in the learning window; p95 alone would hide this workload class.
		cpuTarget = pool.Resources.CPUMillis
	}
	if !canDownsize && memoryTarget < pool.Resources.MemoryBytes {
		memoryTarget = pool.Resources.MemoryBytes
	}
	out.Recommended.CPUMillis = cpuTarget
	out.Recommended.MemoryBytes = memoryTarget
	out.Ready = true
	if pool.Isolation == core.IsolationContainerDinD {
		out.Reason = "CPU p95 and RAM max logical-worker aggregate peaks plus 25% headroom; downsizing requires 25 jobs with at least 2 samples each"
	} else {
		out.Reason = "CPU p95 and RAM max per-container peaks plus 25% headroom; downsizing requires 25 jobs with at least 2 samples each"
	}
	if oomMemoryFloor > out.MemoryPeakMaxBytes {
		out.Reason += "; recent OOM pressure raises the memory floor"
	}
	if cpuTailHeldDownsize {
		out.Reason += fmt.Sprintf("; CPU downsize held because recent max peak %dm exceeds p95-based target %dm", out.CPUPeakMaxMillis, cpuP95Target)
	}
	if out.StaleJobs > 0 {
		out.Reason += fmt.Sprintf("; %d stale telemetry samples ignored (older than 90 days)", out.StaleJobs)
	}
	if out.Jobs >= 25 && out.WellSampledJobs < 25 {
		out.Reason += fmt.Sprintf("; downsize held because only %d jobs are well-sampled", out.WellSampledJobs)
	}
	if out.RejectedJobs > 0 {
		out.Reason += fmt.Sprintf("; %d incompatible or legacy samples ignored", out.RejectedJobs)
	}
	return out
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]int64(nil), values...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	index := (95*len(cp) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(cp) {
		index = len(cp)
	}
	return cp[index-1]
}

func addHeadroom(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return (value*125 + 99) / 100
}

func roundUp(value, step int64) int64 {
	if step <= 0 || value <= 0 {
		return value
	}
	return ((value + step - 1) / step) * step
}

func closeEnough(target, current, step int64) bool {
	if target <= 0 || current <= 0 {
		return false
	}
	diff := target - current
	if diff < 0 {
		diff = -diff
	}
	threshold := current / 10
	if threshold < step {
		threshold = step
	}
	return diff < threshold
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
