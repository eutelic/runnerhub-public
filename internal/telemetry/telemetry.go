package telemetry

import (
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
	if d <= 0 {
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
	cpuStepMillis int64 = 250
	memoryStepBytes int64 = 512 << 20
	minimumCPUMillis int64 = 500
	minimumMemoryBytes int64 = 1 << 30
)

type ResourceRecommendation struct {
	Jobs int `json:"jobs"`
	Ready bool `json:"ready"`
	Confidence string `json:"confidence"`
	Current core.Resources `json:"current"`
	Recommended core.Resources `json:"recommended"`
	CPUPeakP95Millis int64 `json:"cpuPeakP95Millis,omitempty"`
	MemoryPeakP95Bytes int64 `json:"memoryPeakP95Bytes,omitempty"`
	Reason string `json:"reason"`
}

type usagePoint struct {
	at time.Time
	cpu int64
	memory int64
}

func RecommendResources(pool core.RunnerPool, workers []core.Worker) ResourceRecommendation {
	out := ResourceRecommendation{
		Current: pool.Resources,
		Recommended: pool.Resources,
		Reason: "collecting completed-job usage samples",
	}
	points := make([]usagePoint, 0, len(workers))
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
		at := worker.CompletedAt
		if at.IsZero() {
			at = worker.FailedAt
		}
		if at.IsZero() {
			at = worker.UpdatedAt
		}
		points = append(points, usagePoint{at: at, cpu: worker.Usage.PeakCPUMillis, memory: worker.Usage.PeakMemoryBytes})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].at.After(points[j].at) })
	if len(points) > resourceHistoryJobs {
		points = points[:resourceHistoryJobs]
	}
	out.Jobs = len(points)
	switch {
	case out.Jobs >= 25:
		out.Confidence = "high"
	case out.Jobs >= 10:
		out.Confidence = "medium"
	case out.Jobs >= resourceMinimumJobs:
		out.Confidence = "low"
	default:
		out.Confidence = "collecting"
		return out
	}
	cpus := make([]int64, 0, len(points))
	memories := make([]int64, 0, len(points))
	for _, point := range points {
		if point.cpu > 0 {
			cpus = append(cpus, point.cpu)
		}
		if point.memory > 0 {
			memories = append(memories, point.memory)
		}
	}
	if len(cpus) < resourceMinimumJobs || len(memories) < resourceMinimumJobs {
		out.Reason = "not enough complete CPU/RAM samples yet"
		return out
	}
	out.CPUPeakP95Millis = percentile95(cpus)
	out.MemoryPeakP95Bytes = percentile95(memories)
	cpuTarget := roundUp(max64(minimumCPUMillis, addHeadroom(out.CPUPeakP95Millis)), cpuStepMillis)
	memoryTarget := roundUp(max64(minimumMemoryBytes, addHeadroom(out.MemoryPeakP95Bytes)), memoryStepBytes)
	if closeEnough(cpuTarget, pool.Resources.CPUMillis, cpuStepMillis) {
		cpuTarget = pool.Resources.CPUMillis
	}
	if closeEnough(memoryTarget, pool.Resources.MemoryBytes, memoryStepBytes) {
		memoryTarget = pool.Resources.MemoryBytes
	}
	out.Recommended.CPUMillis = cpuTarget
	out.Recommended.MemoryBytes = memoryTarget
	out.Ready = true
	out.Reason = "p95 observed peak plus 25% headroom"
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
	return diff <= threshold
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
