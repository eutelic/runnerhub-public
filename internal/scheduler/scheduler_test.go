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
		Tags: map[string]string{"isolation.container-trusted": "true"},
		Capacity: core.Resources{CPUMillis: 6000, MemoryBytes: 16 << 30, DiskBytes: 30 << 30},
	}
	p := testPool()
	p.Resources = core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30}

	reason := HostRejectionReason(h, p, core.Resources{CPUMillis: 5000, MemoryBytes: 12 << 30, DiskBytes: 20 << 30})
	for _, want := range []string{"cpu exhausted", "memory exhausted", "disk exhausted"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason=%q, missing %q", reason, want)
		}
	}
}
