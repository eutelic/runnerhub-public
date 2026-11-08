package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestPoolHealthSurfacesCapacityDimension(t *testing.T) {
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID:             "pool",
		Isolation:      core.IsolationContainerDinD,
		Resources:      core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
		MaxConcurrency: 2,
	}
	hosts := []core.Host{{
		ID: "host", Name: "truenas-host", Provider: "docker", State: core.HostOnline,
		LastSeenAt: now,
		Tags:       map[string]string{"isolation.container-dind": "true"},
		Capacity:   core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30, DiskBytes: 30 << 30},
	}}
	workers := []core.Worker{{
		ID: "existing", HostID: "host", PoolID: "other", State: core.WorkerBusy,
		ProviderID: "docker", ProviderHandle: "container-existing",
		Isolation: core.IsolationContainerDinD,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
	}}

	health := PoolHealth(pool, hosts, workers, now)
	if health.Healthy {
		t.Fatalf("health unexpectedly healthy: %+v", health)
	}
	reason := strings.Join(health.Reasons, " ")
	if !strings.Contains(reason, "disk exhausted") {
		t.Fatalf("health reasons=%q, want disk exhaustion detail", reason)
	}
}

func TestPoolHealthDoesNotTreatTrustedTmpfsAsHostDisk(t *testing.T) {
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID: "pool", Isolation: core.IsolationContainerTrusted,
		Resources:      core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
		MaxConcurrency: 2,
	}
	hosts := []core.Host{{
		ID: "host", Name: "truenas-host", Provider: "docker", State: core.HostOnline,
		LastSeenAt: now,
		Tags:       map[string]string{"isolation.container-trusted": "true"},
		Capacity:   core.Resources{CPUMillis: 6000, MemoryBytes: 20 << 30, DiskBytes: 25 << 30},
	}}
	workers := []core.Worker{{
		ID: "existing", HostID: "host", PoolID: "other", State: core.WorkerBusy,
		ProviderID: "docker", ProviderHandle: "container-existing",
		Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
	}}
	health := PoolHealth(pool, hosts, workers, now)
	if !health.Healthy {
		t.Fatalf("trusted tmpfs workdir incorrectly made pool unhealthy: %+v", health)
	}
}
