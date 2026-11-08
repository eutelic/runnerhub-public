package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func testPool() core.RunnerPool {
	return core.RunnerPool{
		ID:             "p",
		Name:           "linux",
		Isolation:      core.IsolationContainerTrusted,
		Resources:      core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 2,
	}
}

func putHost(t *testing.T, st store.Store, id string) {
	t.Helper()
	if err := st.UpsertHost(context.Background(), core.Host{
		ID: id, Name: id, Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
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
			Tags:     map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30},
		},
		{
			ID: "large-low-priority", Name: "large-low-priority", Provider: "docker", State: core.HostOnline, Priority: 0,
			Tags:     map[string]string{"isolation.container-trusted": "true"},
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
			Tags:     map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 500, MemoryBytes: 512 << 20},
		},
		{
			ID: "fallback", Name: "fallback", Provider: "docker", State: core.HostOnline, Priority: 0,
			Tags:     map[string]string{"isolation.container-trusted": "true"},
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
		Tags:     map[string]string{"isolation.container-dind": "true"},
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
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30, DiskBytes: 25 << 30},
	}
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30}

	usedWorker := core.Worker{
		ID: "existing", HostID: "a", ProviderID: "docker",
		Isolation: core.IsolationContainerTrusted,
		State:     core.WorkerBusy,
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
		Isolation:  core.IsolationContainerDinD,
		Resources:  resources,
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
		Isolation:  core.IsolationContainerDinD,
		Resources:  core.Resources{DiskBytes: 20 << 30},
	}
	if got := EffectiveWorkerResources(w).DiskBytes; got != 20<<30 {
		t.Fatalf("dind disk accounting=%d, want %d", got, int64(20<<30))
	}
}

func TestSummarizeCapacityAccountsForOtherPools(t *testing.T) {
	host := core.Host{
		ID: "a", Name: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
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
		Tags:     map[string]string{"isolation.container-trusted": "true"},
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

func TestDinDAggregateBudgetUsesSingleEnvelopeOnVerifiedHost(t *testing.T) {
	resources := core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 40 << 30}
	host := core.Host{
		Provider: "docker",
		Tags: map[string]string{
			"isolation.container-dind":        "true",
			"feature.aggregateResourceGroups": "true",
		},
	}
	pool := core.RunnerPool{Isolation: core.IsolationContainerDinD, Resources: resources}
	req := EffectivePoolResources(host, pool)
	if req.CPUMillis != 2500 || req.MemoryBytes != 8<<30 || req.DiskBytes != 40<<30 {
		t.Fatalf("aggregate DinD pool reservation=%+v", req)
	}

	worker := core.Worker{
		ProviderID:         "docker",
		Isolation:          core.IsolationContainerDinD,
		AggregateResources: true,
		Resources:          resources,
	}
	used := EffectiveWorkerResources(worker)
	if used.CPUMillis != 2500 || used.MemoryBytes != 8<<30 || used.DiskBytes != 40<<30 {
		t.Fatalf("aggregate DinD worker reservation=%+v", used)
	}
}

func TestDinDAggregateBudgetDoesNotRetroactivelyChangeLegacyWorker(t *testing.T) {
	resources := core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}
	legacy := core.Worker{
		ProviderID:         "docker",
		Isolation:          core.IsolationContainerDinD,
		AggregateResources: false,
		Resources:          resources,
	}
	used := EffectiveWorkerResources(legacy)
	if used.CPUMillis != 5000 || used.MemoryBytes != 16<<30 {
		t.Fatalf("legacy worker reservation=%+v, want conservative double envelope", used)
	}
}

type liveSnapshotSchedulerStore struct {
	store.Store
	workers []core.Worker
}

func (s liveSnapshotSchedulerStore) ListWorkers(context.Context) ([]core.Worker, error) {
	return nil, errors.New("full worker ledger should not be read")
}

func (s liveSnapshotSchedulerStore) ListNonDeletedWorkers(context.Context) ([]core.Worker, error) {
	return s.workers, nil
}

func TestSelectHostUsesNonDeletedWorkerSnapshot(t *testing.T) {
	base, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	putHost(t, base, "a")

	filtered := liveSnapshotSchedulerStore{Store: base}
	decision, err := (Scheduler{Store: filtered}).SelectHost(context.Background(), testPool())
	if err != nil || decision.Host.ID != "a" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestPlacementDomainSharesCapacityAcrossProviders(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	capacity := core.Resources{CPUMillis: 32000, MemoryBytes: 60 << 30, DiskBytes: 100 << 30}
	for _, h := range []core.Host{
		{ID: "docker-agent", Name: "compute", Provider: "docker", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
		{ID: "process-agent", Name: "compute-android", Provider: "process", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
	} {
		if err := st.UpsertHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "existing", HostID: "docker-agent", PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: core.Resources{CPUMillis: 20000, MemoryBytes: 30 << 30}}); err != nil {
		t.Fatal(err)
	}

	p := testPool()
	p.Resources = core.Resources{CPUMillis: 16000, MemoryBytes: 1 << 30}
	if decision, err := (Scheduler{Store: st}).SelectHost(ctx, p); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("cross-provider shared reservations allowed a second worker: decision=%+v err=%v", decision, err)
	}

	p.Resources = core.Resources{CPUMillis: 12000, MemoryBytes: 1 << 30}
	decision, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if err != nil || decision.Used.CPUMillis != 20000 {
		t.Fatalf("a worker that fits in remaining shared capacity was not admitted: decision=%+v err=%v", decision, err)
	}
	summary := SummarizeCapacity(p, []core.Host{
		{ID: "docker-agent", Provider: "docker", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
		{ID: "process-agent", Provider: "process", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
	}, []core.Worker{{ID: "existing", HostID: "docker-agent", PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: core.Resources{CPUMillis: 20000, MemoryBytes: 30 << 30}}})
	if summary.CompatibleHosts != 2 || summary.PlacementDomains != 1 || summary.Capacity.CPUMillis != capacity.CPUMillis || summary.Reserved.CPUMillis != 20000 || summary.AdditionalWorkers != 1 {
		t.Fatalf("shared physical capacity summary=%+v", summary)
	}
}

func TestOfflinePlacementMemberDoesNotEraseLiveCapacityOrReservations(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	ctx := context.Background()
	capacity := core.Resources{CPUMillis: 32000, MemoryBytes: 60 << 30, DiskBytes: 100 << 30}
	for _, h := range []core.Host{
		{ID: "docker-agent", Name: "compute", Provider: "docker", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
		{ID: "process-agent", Name: "compute-android", Provider: "process", State: core.HostOffline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{}},
		{ID: "stale-docker-agent", Name: "compute-old", Provider: "docker", State: core.HostDraining, PlacementDomainID: "machine-01", Capacity: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}},
	} {
		if err := st.UpsertHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "offline-worker", HostID: "process-agent", PoolID: "other", ProviderID: "process", State: core.WorkerBusy, Resources: core.Resources{CPUMillis: 24000, MemoryBytes: 20 << 30}}); err != nil {
		t.Fatal(err)
	}
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 8000, MemoryBytes: 1 << 30}
	decision, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if err != nil || decision.Host.ID != "docker-agent" || decision.Used.CPUMillis != 24000 {
		t.Fatalf("offline member capacity or reservation mishandled: decision=%+v err=%v", decision, err)
	}
	summary := SummarizeCapacity(p, []core.Host{
		{ID: "docker-agent", Name: "compute", Provider: "docker", State: core.HostOnline, PlacementDomainID: "machine-01", Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: capacity},
		{ID: "process-agent", Name: "compute-android", Provider: "process", State: core.HostOffline, PlacementDomainID: "machine-01", Capacity: core.Resources{}},
		{ID: "stale-docker-agent", Name: "compute-old", Provider: "docker", State: core.HostDraining, PlacementDomainID: "machine-01", Capacity: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}},
	}, []core.Worker{{ID: "offline-worker", HostID: "process-agent", PoolID: "other", ProviderID: "process", State: core.WorkerBusy, Resources: core.Resources{CPUMillis: 24000, MemoryBytes: 20 << 30}}})
	if summary.Capacity.CPUMillis != capacity.CPUMillis || summary.Reserved.CPUMillis != 24000 || summary.PlacementDomains != 1 || summary.AdditionalWorkers != 1 {
		t.Fatalf("offline shared capacity summary=%+v", summary)
	}
}
