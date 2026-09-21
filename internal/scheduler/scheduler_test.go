package scheduler

import (
	"context"
	"errors"
	"testing"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func testPool() core.RunnerPool {
	return core.RunnerPool{
		ID:        "p",
		Name:      "linux",
		Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 2,
	}
}

func putHost(t *testing.T, st store.Store, id string) {
	t.Helper()
	if err := st.UpsertHost(context.Background(), core.Host{
		ID: id, Name: id, Provider: "docker", State: core.HostOnline,
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSelectHost(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	putHost(t, st, "a")
	d, err := (Scheduler{Store: st}).SelectHost(context.Background(), testPool())
	if err != nil || d.Host.ID != "a" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}

func TestSelectHostPrefersHigherPriorityBeforeCapacity(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	for _, h := range []core.Host{
		{
			ID: "fast", Name: "fast", Provider: "docker", State: core.HostOnline, Priority: 100,
			Tags: map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30},
		},
		{
			ID: "large-low-priority", Name: "large-low-priority", Provider: "docker", State: core.HostOnline, Priority: 0,
			Tags: map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 32000, MemoryBytes: 64 << 30},
		},
	} {
		if err := st.UpsertHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	d, err := (Scheduler{Store: st}).SelectHost(ctx, testPool())
	if err != nil {
		t.Fatal(err)
	}
	if d.Host.ID != "fast" {
		t.Fatalf("selected host=%q, want higher-priority fast host; reason=%s", d.Host.ID, d.Reason)
	}
}

func TestSelectHostFallsBackWhenHigherPriorityHostCannotFit(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	for _, h := range []core.Host{
		{
			ID: "fast", Name: "fast", Provider: "docker", State: core.HostOnline, Priority: 100,
			Tags: map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 500, MemoryBytes: 512 << 20},
		},
		{
			ID: "fallback", Name: "fallback", Provider: "docker", State: core.HostOnline, Priority: 0,
			Tags: map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
		},
	} {
		if err := st.UpsertHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	d, err := (Scheduler{Store: st}).SelectHost(ctx, testPool())
	if err != nil {
		t.Fatal(err)
	}
	if d.Host.ID != "fallback" {
		t.Fatalf("selected host=%q, want compatible fallback host", d.Host.ID)
	}
}

func TestPoolConcurrencyIsGlobalAcrossHosts(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	putHost(t, st, "a")
	putHost(t, st, "b")
	p := testPool()
	for i, host := range []string{"a", "b"} {
		if err := st.UpsertWorker(ctx, core.Worker{
			ID: string(rune('1' + i)), PoolID: p.ID, HostID: host,
			State: core.WorkerBusy, Resources: p.Resources,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("expected pool concurrency exhaustion, got %v", err)
	}
}

func TestProviderBackedCleanupStillOccupiesHostResources(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	putHost(t, st, "a")
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}
	if err := st.UpsertWorker(ctx, core.Worker{
		ID: "old", PoolID: "other", HostID: "a",
		State: core.WorkerCleanupPending, ProviderHandle: "container-old",
		Resources: p.Resources,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("provider-backed cleanup worker must reserve capacity, got %v", err)
	}
}

func TestFailedWorkerWithoutProviderHandleReleasesHostCapacity(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	putHost(t, st, "a")
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}
	if err := st.UpsertWorker(ctx, core.Worker{
		ID: "failed-before-create", PoolID: "other", HostID: "a",
		State: core.WorkerFailed, Resources: p.Resources,
	}); err != nil {
		t.Fatal(err)
	}
	d, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if err != nil {
		t.Fatalf("failed worker without provider handle blocked capacity: %v", err)
	}
	if d.Host.ID != "a" {
		t.Fatalf("selected host=%q, want a", d.Host.ID)
	}
}

func TestHostRejectionReasonNamesExhaustedDimensions(t *testing.T) {
	h := core.Host{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags: map[string]string{"isolation.container-dind": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30, DiskBytes: 30 << 30},
	}
	p := testPool()
	p.Isolation = core.IsolationContainerDinD
	p.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30}

	reason := HostRejectionReason(h, p, core.Resources{CPUMillis: 5000, MemoryBytes: 12 << 30, DiskBytes: 20 << 30})
	for _, want := range []string{"cpu exhausted", "memory exhausted", "disk exhausted"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason=%q, missing %q", reason, want)
		}
	}
}

func TestTrustedDockerTmpfsDoesNotConsumeHostDiskCapacity(t *testing.T) {
	h := core.Host{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30, DiskBytes: 25 << 30},
	}
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30}

	usedWorker := core.Worker{
		ID: "existing", HostID: "a", ProviderID: "docker",
		Isolation: core.IsolationContainerTrusted,
		State: core.WorkerBusy,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
	}
	used := EffectiveWorkerResources(usedWorker)
	if used.DiskBytes != 0 {
		t.Fatalf("trusted Docker worker disk accounting=%d, want 0", used.DiskBytes)
	}
	req := EffectivePoolResources(h, p)
	if req.DiskBytes != 0 {
		t.Fatalf("trusted Docker pool disk accounting=%d, want 0", req.DiskBytes)
	}
	if reason := HostRejectionReason(h, p, used); strings.Contains(reason, "disk exhausted") {
		t.Fatalf("trusted tmpfs workdir incorrectly exhausted host disk: %q", reason)
	}
}

func TestDinDReservesRunnerAndSidecarCPUAndMemory(t *testing.T) {
	resources := core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 40 << 30}
	worker := core.Worker{
		ProviderID: "docker",
		Isolation: core.IsolationContainerDinD,
		Resources: resources,
	}
	used := EffectiveWorkerResources(worker)
	if used.CPUMillis != 5000 || used.MemoryBytes != 16<<30 || used.DiskBytes != 40<<30 {
		t.Fatalf("DinD worker reservation=%+v", used)
	}

	host := core.Host{Provider: "docker"}
	pool := core.RunnerPool{Isolation: core.IsolationContainerDinD, Resources: resources}
	req := EffectivePoolResources(host, pool)
	if req.CPUMillis != 5000 || req.MemoryBytes != 16<<30 || req.DiskBytes != 40<<30 {
		t.Fatalf("DinD pool reservation=%+v", req)
	}
}

func TestDinDStillConsumesHostDiskCapacity(t *testing.T) {
	w := core.Worker{
		ProviderID: "docker",
		Isolation: core.IsolationContainerDinD,
		Resources: core.Resources{DiskBytes: 20 << 30},
	}
	if got := EffectiveWorkerResources(w).DiskBytes; got != 20<<30 {
		t.Fatalf("dind disk accounting=%d, want %d", got, int64(20<<30))
	}
}


func TestSummarizeCapacityAccountsForOtherPools(t *testing.T) {
	host := core.Host{
		ID: "a", Name: "a", Provider: "docker", State: core.HostOnline,
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30},
	}
	pool := testPool()
	pool.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30}
	pool.MaxConcurrency = 4
	workers := []core.Worker{{
		ID: "other", PoolID: "other", HostID: "a", ProviderID: "docker",
		Isolation: core.IsolationContainerTrusted, State: core.WorkerBusy,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30},
	}}

	got := SummarizeCapacity(pool, []core.Host{host}, workers)
	if got.CompatibleHosts != 1 || got.AdditionalWorkers != 1 || got.CapacityLimit != 1 {
		t.Fatalf("capacity summary=%+v", got)
	}
	if got.Reserved.CPUMillis != 2500 || got.Reserved.MemoryBytes != 4<<30 {
		t.Fatalf("reserved=%+v", got.Reserved)
	}
	if got.Bottleneck != "host_resources" {
		t.Fatalf("bottleneck=%q, want host_resources", got.Bottleneck)
	}
}

func TestSummarizeCapacityIdentifiesPoolLimit(t *testing.T) {
	host := core.Host{
		ID: "a", Name: "a", Provider: "docker", State: core.HostOnline,
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30},
	}
	pool := testPool()
	pool.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30}
	pool.MaxConcurrency = 1

	got := SummarizeCapacity(pool, []core.Host{host}, nil)
	if got.AdditionalWorkers != 2 || got.CapacityLimit != 2 {
		t.Fatalf("capacity summary=%+v", got)
	}
	if got.Bottleneck != "pool_concurrency" {
		t.Fatalf("bottleneck=%q, want pool_concurrency", got.Bottleneck)
	}
}
