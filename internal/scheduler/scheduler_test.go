package scheduler

import (
	"context"
	"errors"
	"fmt"
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

func TestSelectHostEmptyFleetExplainsCapacityFailure(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = (Scheduler{Store: st}).SelectHost(context.Background(), testPool())
	if !errors.Is(err, ErrNoCapacity) || !strings.Contains(err.Error(), "no hosts registered") {
		t.Fatalf("empty fleet error = %v, want actionable no-capacity explanation", err)
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

func TestSelectHostEqualCapacityTieIsDeterministic(t *testing.T) {
	for _, order := range [][]string{{"b", "a"}, {"a", "b"}} {
		st, err := store.OpenSQLite(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range order {
			putHost(t, st, id)
		}
		decision, err := (Scheduler{Store: st}).SelectHost(context.Background(), testPool())
		st.Close()
		if err != nil || decision.Host.ID != "a" {
			t.Fatalf("host order=%v: decision=%+v err=%v; want a", order, decision, err)
		}
	}
}

func TestSelectHostUnlimitedConcurrencyStillRespectsPhysicalCapacity(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	putHost(t, st, "a")
	p := testPool()
	p.MaxConcurrency = 0 // Unlimited pool concurrency, not zero allowed workers.
	for i := 0; i < 2; i++ {
		if err := st.UpsertWorker(ctx, core.Worker{
			ID: fmt.Sprintf("existing-%d", i), PoolID: p.ID, HostID: "a",
			State: core.WorkerBusy, Resources: p.Resources,
		}); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if err != nil || decision.Host.ID != "a" {
		t.Fatalf("unlimited pool rejected available host: decision=%+v err=%v", decision, err)
	}
	p.Resources = core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}
	if _, err := (Scheduler{Store: st}).SelectHost(ctx, p); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("unlimited pool must still enforce host resources, got %v", err)
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
	if got.AdditionalWorkers != 1 || got.CapacityLimit != 1 {
		t.Fatalf("capacity summary=%+v", got)
	}
	if got.Bottleneck != "pool_concurrency" {
		t.Fatalf("bottleneck=%q, want pool_concurrency", got.Bottleneck)
	}
}

func TestSummarizeCapacityFullPoolAdvertisesNoAdditionalAdmission(t *testing.T) {
	host := core.Host{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 10000, MemoryBytes: 16 << 30},
	}
	pool := testPool()
	pool.MaxConcurrency = 1
	workers := []core.Worker{{
		ID: "running", PoolID: pool.ID, HostID: host.ID,
		State: core.WorkerBusy, Resources: pool.Resources,
	}}
	got := SummarizeCapacity(pool, []core.Host{host}, workers)
	if got.AdditionalWorkers != 0 || got.CapacityLimit != 1 || got.Bottleneck != "pool_concurrency" {
		t.Fatalf("full pool advertised new admissions: %+v", got)
	}
}

func TestSummarizeCapacityUnlimitedPoolKeepsPhysicalLimit(t *testing.T) {
	host := core.Host{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30},
	}
	pool := testPool()
	pool.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 4 << 30}
	pool.MaxConcurrency = 0
	got := SummarizeCapacity(pool, []core.Host{host}, nil)
	if got.AdditionalWorkers != 2 || got.CapacityLimit != 2 || got.Bottleneck != "host_resources" {
		t.Fatalf("unlimited pool capacity summary=%+v", got)
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

func TestSelectHostRejectionsAreStableAcrossHostInsertionOrder(t *testing.T) {
	var first string
	for _, order := range [][]string{{"z", "a"}, {"a", "z"}} {
		st, err := store.OpenSQLite(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, id := range order {
			if err := st.UpsertHost(ctx, core.Host{
				ID: id, Name: id, Provider: "docker", State: core.HostOffline,
				Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
			}); err != nil {
				t.Fatal(err)
			}
		}
		_, err = (Scheduler{Store: st}).SelectHost(ctx, testPool())
		st.Close()
		if !errors.Is(err, ErrNoCapacity) {
			t.Fatalf("order=%v: got %v", order, err)
		}
		if first == "" {
			first = err.Error()
		} else if first != err.Error() {
			t.Fatalf("rejection changed with host order: %q != %q", first, err)
		}
		if !strings.Contains(err.Error(), "a: not online; z: not online") {
			t.Fatalf("rejections not sorted: %v", err)
		}
	}
}

func TestSummarizeCapacityNeverFallsBelowActiveWorkers(t *testing.T) {
	host := core.Host{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 10000, MemoryBytes: 16 << 30},
	}
	pool := testPool()
	pool.MaxConcurrency = 1
	pool.Resources = core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}
	workers := []core.Worker{
		{ID: "one", PoolID: pool.ID, HostID: "a", ProviderID: "docker", State: core.WorkerBusy, Resources: pool.Resources},
		{ID: "two", PoolID: pool.ID, HostID: "a", ProviderID: "docker", State: core.WorkerReady, Resources: pool.Resources},
	}
	got := SummarizeCapacity(pool, []core.Host{host}, workers)
	if got.ActiveWorkers != 2 || got.CapacityLimit != 2 || got.Bottleneck != "pool_concurrency" {
		t.Fatalf("capacity dropped below existing workers: %+v", got)
	}
}

func TestSelectHostRejectsNegativeResourceReservations(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*core.Resources)
	}{
		{"cpu", func(r *core.Resources) { r.CPUMillis = -1 }},
		{"memory", func(r *core.Resources) { r.MemoryBytes = -1 }},
		{"disk", func(r *core.Resources) { r.DiskBytes = -1 }},
		{"gpu", func(r *core.Resources) { r.GPUs = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.OpenSQLite(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			putHost(t, st, "a")
			pool := testPool()
			tc.set(&pool.Resources)
			_, err = (Scheduler{Store: st}).SelectHost(context.Background(), pool)
			if err == nil || !strings.Contains(err.Error(), "negative reservation") {
				t.Fatalf("negative %s reservation accepted: %v", tc.name, err)
			}
		})
	}
}

func TestSelectHostInvalidReservationIsNotRetryableWhenPoolFull(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	putHost(t, st, "a")
	p := testPool()
	p.MaxConcurrency = 1
	if err := st.UpsertWorker(ctx, core.Worker{
		ID: "existing", PoolID: p.ID, HostID: "a",
		State: core.WorkerBusy, Resources: p.Resources,
	}); err != nil {
		t.Fatal(err)
	}
	p.Resources.CPUMillis = -1
	_, err = (Scheduler{Store: st}).SelectHost(ctx, p)
	if err == nil || !strings.Contains(err.Error(), "invalid pool resources") {
		t.Fatalf("invalid reservation must be reported before concurrency: %v", err)
	}
	if errors.Is(err, ErrNoCapacity) {
		t.Fatalf("invalid reservation must not be treated as retryable capacity shortage: %v", err)
	}
}

func TestSummarizeCapacityRejectsNegativeReservations(t *testing.T) {
	pool := testPool()
	pool.Resources.CPUMillis = -1000
	host := core.Host{ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}}
	got := SummarizeCapacity(pool, []core.Host{host}, nil)
	if got.Bottleneck != "invalid_pool_resources" || got.AdditionalWorkers != 0 || got.CapacityLimit != 0 {
		t.Fatalf("invalid pool advertised available capacity: %+v", got)
	}
}

func TestNegativePoolConcurrencyFailsClosed(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	putHost(t, st, "a")
	pool := testPool()
	pool.MaxConcurrency = -1
	_, err = (Scheduler{Store: st}).SelectHost(context.Background(), pool)
	if err == nil || !strings.Contains(err.Error(), "invalid pool max concurrency") {
		t.Fatalf("negative concurrency must not admit work: %v", err)
	}
	if errors.Is(err, ErrNoCapacity) {
		t.Fatalf("invalid configuration must not appear retryable: %v", err)
	}
	summary := SummarizeCapacity(pool, []core.Host{{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
	}}, nil)
	if summary.Bottleneck != "invalid_pool_concurrency" || summary.AdditionalWorkers != 0 || summary.CapacityLimit != 0 {
		t.Fatalf("negative concurrency advertised schedulable capacity: %+v", summary)
	}
}

func TestResourceFitCountClampsLargeHostCapacity(t *testing.T) {
	// A 32-bit control-plane build must not turn a large physical slot
	// count into a negative value when converting from int64 to int.
	const maxInt64 = int64(9223372036854775807)
	got := resourceFitCount(core.Resources{CPUMillis: maxInt64}, core.Resources{}, core.Resources{CPUMillis: 1})
	want := int(^uint(0) >> 1)
	if got != want {
		t.Fatalf("large capacity slots=%d, want saturated %d", got, want)
	}
}

func TestSummarizeCapacitySaturatesAggregateDomainSlots(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	pool := testPool()
	pool.MaxConcurrency = 0
	pool.Resources = core.Resources{CPUMillis: 1}
	hosts := []core.Host{
		{ID: "a", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64}},
		{ID: "b", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64}},
	}
	got := SummarizeCapacity(pool, hosts, nil)
	maxInt := int(^uint(0) >> 1)
	if got.AdditionalWorkers != maxInt || got.CapacityLimit != maxInt {
		t.Fatalf("aggregate domain capacity overflowed: %+v", got)
	}
}

func TestSummarizeCapacitySaturatesLimitWithActiveWorkers(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	pool := testPool()
	pool.MaxConcurrency = 0
	pool.Resources = core.Resources{CPUMillis: 1}
	hosts := []core.Host{
		{ID: "a", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64}},
		{ID: "b", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64}},
	}
	workers := []core.Worker{
		{ID: "existing", PoolID: pool.ID, HostID: "a", ProviderID: "docker", State: core.WorkerBusy, Resources: pool.Resources},
	}
	got := SummarizeCapacity(pool, hosts, workers)
	maxInt := int(^uint(0) >> 1)
	if got.ActiveWorkers != 1 || got.AdditionalWorkers != maxInt-1 || got.CapacityLimit != maxInt {
		t.Fatalf("active worker plus saturated physical slots overflowed: %+v", got)
	}
}

func TestZeroResourcePoolFailsClosed(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	putHost(t, st, "a")
	pool := testPool()
	pool.Resources = core.Resources{}
	_, err = (Scheduler{Store: st}).SelectHost(context.Background(), pool)
	if err == nil || !strings.Contains(err.Error(), "no positive reservation") {
		t.Fatalf("zero-resource pool admitted: %v", err)
	}
	if errors.Is(err, ErrNoCapacity) {
		t.Fatalf("invalid zero reservation must not be retryable: %v", err)
	}
	summary := SummarizeCapacity(pool, []core.Host{{
		ID: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
	}}, nil)
	if summary.AdditionalWorkers != 0 || summary.CapacityLimit != 0 || summary.Bottleneck != "invalid_pool_resources" {
		t.Fatalf("zero-resource pool advertised capacity: %+v", summary)
	}
}

func TestResourceFitCountOverReservedDoesNotOverflow(t *testing.T) {
	const maxInt64 = int64(1<<63 - 1)
	capacity := core.Resources{CPUMillis: 1000, MemoryBytes: 1024, DiskBytes: 100, GPUs: 1}
	requested := core.Resources{CPUMillis: 100, MemoryBytes: 128, DiskBytes: 10, GPUs: 1}
	for _, tc := range []struct {
		name string
		used core.Resources
	}{
		{"cpu", core.Resources{CPUMillis: maxInt64}},
		{"memory", core.Resources{MemoryBytes: maxInt64}},
		{"disk", core.Resources{DiskBytes: maxInt64}},
		{"gpu", core.Resources{GPUs: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if slots := resourceFitCount(capacity, tc.used, requested); slots != 0 {
				t.Fatalf("over-reserved %s reported %d free slots, want zero", tc.name, slots)
			}
		})
	}
}

func TestHostRejectionOverReservedDoesNotOverflow(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	host := core.Host{
		ID: "over-reserved", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 1000, MemoryBytes: 1024, DiskBytes: 1024, GPUs: 1},
	}
	pool := testPool()
	pool.Resources = core.Resources{CPUMillis: 1, MemoryBytes: 1, DiskBytes: 1, GPUs: 1}
	used := core.Resources{CPUMillis: maxInt64, MemoryBytes: maxInt64, DiskBytes: maxInt64, GPUs: 2147483647}
	reason := HostRejectionReason(host, pool, used)
	for _, dimension := range []string{"cpu exhausted", "memory exhausted", "gpu exhausted"} {
		if !strings.Contains(reason, dimension) {
			t.Fatalf("over-reserved host incorrectly admitted %s: %q", dimension, reason)
		}
	}
	// Trusted Docker workers mount their workdir on tmpfs, so disk is not an
	// effective reservation for this host. Check disk accounting separately.
	if diskReason := resourceExhaustionReason(host.Capacity, used, pool.Resources); !strings.Contains(diskReason, "disk exhausted") {
		t.Fatalf("over-reserved disk was not rejected: %q", diskReason)
	}
	if slots := resourceFitCount(host.Capacity, used, pool.Resources); slots != 0 {
		t.Fatalf("over-reserved host advertised %d slots", slots)
	}
}

func TestWorkerReservationsSaturateWithoutAdvertisingCapacity(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	host := core.Host{
		ID: "a", Name: "a", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: maxInt64, MemoryBytes: maxInt64, DiskBytes: maxInt64, GPUs: 4},
	}
	pool := testPool()
	pool.MaxConcurrency = 0
	pool.Resources = core.Resources{CPUMillis: 1, MemoryBytes: 1, DiskBytes: 1}
	reservation := core.Resources{CPUMillis: maxInt64 - 1, MemoryBytes: maxInt64 - 1, DiskBytes: maxInt64 - 1}
	workers := []core.Worker{
		{ID: "w1", HostID: host.ID, PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: reservation},
		{ID: "w2", HostID: host.ID, PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: reservation},
	}
	summary := SummarizeCapacity(pool, []core.Host{host}, workers)
	if summary.AdditionalWorkers != 0 || summary.CapacityLimit != 0 || summary.Reserved.CPUMillis != maxInt64 {
		t.Fatalf("overflowed reservations advertised available capacity: %+v", summary)
	}
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	for _, worker := range workers {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (Scheduler{Store: st}).SelectHost(ctx, pool); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("overflowed reservations admitted worker: %v", err)
	}
}

func TestReservationAggregationRejectsNegativeAndSaturatesGPU(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	got := addReservationsSaturating(core.Resources{CPUMillis: -1, GPUs: maxInt}, core.Resources{CPUMillis: 1, GPUs: 1})
	if got.CPUMillis != int64(^uint64(0)>>1) || got.GPUs != maxInt {
		t.Fatalf("malformed or overflowing reservations did not fail closed: %+v", got)
	}
}

func TestSummarizeCapacitySaturatesAggregateResourceTotals(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	pool := testPool()
	pool.MaxConcurrency = 0
	pool.Resources = core.Resources{CPUMillis: 1}
	hosts := []core.Host{
		{ID: "a", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64, MemoryBytes: maxInt64, DiskBytes: maxInt64, GPUs: 2}},
		{ID: "b", Provider: "docker", State: core.HostOnline, Tags: map[string]string{"isolation.container-trusted": "true"}, Capacity: core.Resources{CPUMillis: maxInt64, MemoryBytes: maxInt64, DiskBytes: maxInt64, GPUs: 2}},
	}
	reservation := core.Resources{CPUMillis: maxInt64 - 1, MemoryBytes: maxInt64 - 1, DiskBytes: maxInt64 - 1, GPUs: 2}
	workers := []core.Worker{
		{ID: "w1", HostID: "a", PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: reservation},
		{ID: "w2", HostID: "b", PoolID: "other", ProviderID: "docker", State: core.WorkerBusy, Resources: reservation},
	}
	got := SummarizeCapacity(pool, hosts, workers)
	if got.Capacity.CPUMillis != maxInt64 || got.Capacity.MemoryBytes != maxInt64 || got.Capacity.DiskBytes != maxInt64 || got.Capacity.GPUs != 4 {
		t.Fatalf("aggregate domain capacity wrapped: %+v", got.Capacity)
	}
	if got.Reserved.CPUMillis != maxInt64 || got.Reserved.MemoryBytes != maxInt64 || got.Reserved.DiskBytes != maxInt64 || got.Reserved.GPUs != 4 {
		t.Fatalf("aggregate domain reservations wrapped: %+v", got.Reserved)
	}
	if got.AdditionalWorkers != 2 {
		t.Fatalf("unexpected available slots after domain aggregation: %+v", got)
	}
}

func TestNegativeHostAccountingFailsClosed(t *testing.T) {
	host := core.Host{
		ID: "negative-accounting", Name: "negative-accounting",
		Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30, DiskBytes: 1000, GPUs: 2},
	}
	pool := testPool()
	pool.Resources = core.Resources{CPUMillis: 1, MemoryBytes: 1, DiskBytes: 1, GPUs: 1}
	for _, tc := range []struct {
		name string
		used core.Resources
	}{
		{"cpu", core.Resources{CPUMillis: -1}},
		{"memory", core.Resources{MemoryBytes: -1}},
		{"disk", core.Resources{DiskBytes: -1}},
		{"gpu", core.Resources{GPUs: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceFitCount(host.Capacity, tc.used, pool.Resources); got != 0 {
				t.Fatalf("negative %s accounting advertised %d slots", tc.name, got)
			}
			reason := HostRejectionReason(host, pool, tc.used)
			if tc.name == "disk" {
				// Trusted Docker workdirs use tmpfs, so evaluate the disk
				// reservation directly rather than requiring host rejection.
				reason = resourceExhaustionReason(host.Capacity, tc.used, pool.Resources)
			}
			if !strings.Contains(reason, tc.name+" exhausted") {
				t.Fatalf("negative %s accounting admitted host: %q", tc.name, reason)
			}
		})
	}
}

func TestSelectHostLargeResidualScoreDoesNotOverflow(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p := testPool()
	p.MaxConcurrency = 0
	const maxInt64 = int64(^uint64(0) >> 1)
	for _, h := range []core.Host{
		{ID: "small", Name: "small", Provider: "docker", State: core.HostOnline,
			Tags:     map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}},
		{ID: "large", Name: "large", Provider: "docker", State: core.HostOnline,
			Tags:     map[string]string{"isolation.container-trusted": "true"},
			Capacity: core.Resources{CPUMillis: maxInt64, MemoryBytes: maxInt64}},
	} {
		if err := st.UpsertHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	d, err := (Scheduler{Store: st}).SelectHost(ctx, p)
	if err != nil || d.Host.ID != "large" {
		t.Fatalf("large host should not rank below small host after score overflow: decision=%+v err=%v", d, err)
	}
}

func TestDiskOnlyTmpfsPoolCannotAdmitUnaccountedWorkers(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	h := core.Host{ID: "tmpfs", Name: "tmpfs", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30, DiskBytes: 100 << 30}}
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	p := testPool()
	p.Isolation = core.IsolationContainerTrusted
	p.Resources = core.Resources{DiskBytes: 1 << 30}
	p.MaxConcurrency = 0
	if decision, err := (Scheduler{Store: st}).SelectHost(ctx, p); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("zero effective reservation admitted: decision=%+v err=%v", decision, err)
	}
	got := SummarizeCapacity(p, []core.Host{h}, nil)
	if got.AdditionalWorkers != 0 || got.CapacityLimit != 0 {
		t.Fatalf("zero effective reservation advertised capacity: %+v", got)
	}
}
