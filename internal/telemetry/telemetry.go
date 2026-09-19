package telemetry

import (
	"sync"
	"time"
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
