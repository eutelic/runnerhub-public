package fleet

import (
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestEligibleCapacityCeilingTracksFreshHostsAndSharedReservations(t *testing.T) {
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID: "pool-a", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 4,
	}
	host := core.Host{
		ID: "compute", Name: "compute", Provider: "docker",
		State: core.HostOnline, LastSeenAt: now,
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 3000, MemoryBytes: 4 << 30},
	}
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 3 {
		t.Fatalf("advertised empty-host capacity=%d, want 3 physical slots", got)
	}
	busy := core.Worker{
		ID: "other-worker", PoolID: "pool-b", HostID: host.ID,
		State: core.WorkerBusy, Isolation: core.IsolationContainerTrusted,
		ProviderID: "docker", ProviderHandle: "docker-other",
		Resources: core.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30},
	}
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, []core.Worker{busy}, now); got != 1 {
		t.Fatalf("shared-host contention capacity=%d, want 1", got)
	}
	host.LastSeenAt = now.Add(-2 * time.Minute)
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 0 {
		t.Fatalf("stale host advertised capacity=%d, want 0", got)
	}
	host.LastSeenAt = now
	host.State = core.HostDraining
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 0 {
		t.Fatalf("draining host advertised capacity=%d, want 0", got)
	}
	host.State = core.HostOnline
	ourWorker := core.Worker{
		ID: "ours", PoolID: pool.ID, HostID: host.ID, State: core.WorkerBusy,
		ProviderID: "docker", ProviderHandle: "docker-ours",
		Isolation: core.IsolationContainerTrusted, Resources: pool.Resources,
	}
	host.LastSeenAt = now.Add(-2 * time.Minute)
	// Existing reservations are still counted when the host goes offline,
	// but the unavailable machine must not add phantom fresh capacity.
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, []core.Worker{ourWorker}, now); got != 1 {
		t.Fatalf("stale host with running worker capacity=%d, want 1", got)
	}
}

func TestEligibleCapacityCeilingHonorsPoolLimitAndDinDAccounting(t *testing.T) {
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID: "dind", Isolation: core.IsolationContainerDinD,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30},
		MaxConcurrency: 4,
	}
	host := core.Host{
		ID: "compute", Name: "compute", Provider: "docker",
		State: core.HostOnline, LastSeenAt: now,
		Tags: map[string]string{"isolation.container-dind": "true"},
		Capacity: core.Resources{CPUMillis: 10000, MemoryBytes: 32 << 30},
	}
	// Conservative non-aggregate DinD reservations count twice per worker:
	// 10 CPUs / (2 x 2.5 CPUs) = 2, not 4.
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 2 {
		t.Fatalf("non-aggregate DinD capacity=%d, want 2", got)
	}
	host.Tags["feature.aggregateResourceGroups"] = "true"
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 4 {
		t.Fatalf("verified aggregate DinD capacity=%d, want pool max 4", got)
	}
	pool.MaxConcurrency = 1
	if got := EligibleCapacityCeiling(pool, []core.Host{host}, nil, now); got != 1 {
		t.Fatalf("pool concurrency cap=%d, want 1", got)
	}
}

func TestEligibleCapacityCeilingPreservesStaleAndArchivedHostReservations(t *testing.T) {
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID: "pool-a", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 4,
	}
	live := core.Host{
		ID: "current-agent", Name: "current-agent", Provider: "docker",
		State: core.HostOnline, LastSeenAt: now,
		PlacementDomainID: "physical-compute",
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 3000, MemoryBytes: 4 << 30},
	}
	old := live
	old.ID = "stale-agent"
	old.Name = "stale-agent"
	old.LastSeenAt = now.Add(-2 * time.Minute)

	// An existing worker belongs to a stale enrollment for the SAME
	// physical machine. Its CPU reservation must reduce current free slots.
	reserved := core.Worker{
		ID: "other-worker", PoolID: "other-pool", HostID: old.ID,
		State: core.WorkerBusy, ProviderID: "docker",
		ProviderHandle: "old-container",
		Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30},
	}
	if got := EligibleCapacityCeiling(pool, []core.Host{old,live}, []core.Worker{reserved}, now); got != 1 {
		t.Fatalf("stale-alias worker reservation lost: capacity=%d, want 1", got)
	}

	// Host merge/archive must retain the old enrollment's mapping through
	// MergedIntoHostID, even though the archived host is ineligible to start.
	old.State = core.HostArchived
	old.PlacementDomainID = ""
	old.MergedIntoHostID = live.ID
	if got := EligibleCapacityCeiling(pool, []core.Host{old,live}, []core.Worker{reserved}, now); got != 1 {
		t.Fatalf("archived-alias worker reservation lost: capacity=%d, want 1", got)
	}
}
