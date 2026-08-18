package api

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/runnerhub/internal/agent"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"github.com/example-user/runnerhub/internal/store"
)

func TestCreateResultMovesWorkerToRegistering(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	worker := core.Worker{
		ID:        "worker-create",
		HostID:    "host-1",
		ProviderID: "docker",
		State:     core.WorkerProvisioning,
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st}
	handle := provider.WorkerHandle{ProviderID: "container-123", HostID: "host-1"}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: worker.ID,
		OK:       true,
		Handle:   &handle,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerRegistering {
		t.Fatalf("create result should move worker to registering, got %s", got.State)
	}
	if got.ProviderHandle != handle.ProviderID {
		t.Fatalf("provider handle=%q, want %q", got.ProviderHandle, handle.ProviderID)
	}
}

func TestProviderReadyDoesNotBypassGitHubRegistration(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	worker := core.Worker{
		ID:             "worker-registering",
		HostID:         "host-1",
		ProviderID:     "docker",
		ProviderHandle: "container-123",
		State:          core.WorkerRegistering,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st}
	providerState := provider.WorkerState{State: core.WorkerReady, SafeDetail: "container running"}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: worker.ID,
		OK:       true,
		State:    &providerState,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerRegistering {
		t.Fatalf("provider readiness must not bypass GitHub registration, got %s", got.State)
	}
}
