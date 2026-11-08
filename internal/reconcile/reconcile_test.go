package reconcile

import (
	"context"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
	"testing"
	"time"
)

func TestProvisionTimeoutDefaultsToTenMinutes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	w := core.Worker{ID: "w", State: core.WorkerProvisioning, UpdatedAt: now.Add(-3 * time.Minute)}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Store: st, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorker(ctx, "w")
	if got.State != core.WorkerProvisioning {
		t.Fatalf("slow healthy provisioning was failed after three minutes: %s", got.State)
	}
	w.UpdatedAt = now.Add(-11 * time.Minute)
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetWorker(ctx, "w")
	if got.State != core.WorkerFailed {
		t.Fatalf("expected timeout after eleven minutes, got %s", got.State)
	}
}
func TestProvisionTimeoutCanBeTightened(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	now := time.Now().UTC()
	_ = st.UpsertWorker(ctx, core.Worker{ID: "w", State: core.WorkerProvisioning, UpdatedAt: now.Add(-2 * time.Minute)})
	r := &Reconciler{Store: st, ProvisionTimeout: time.Minute, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorker(ctx, "w")
	if got.State != core.WorkerFailed {
		t.Fatalf("custom timeout not honored: %s", got.State)
	}
}
