package api

import (
	"context"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/store"
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
	if got.RegistrationStartedAt.IsZero() {
		t.Fatal("create result did not record registration start time")
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


func TestProviderFailureRetainsExitCode(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	worker := core.Worker{
		ID:             "worker-failed",
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
	exitCode := 13
	providerState := provider.WorkerState{State: core.WorkerFailed, ExitCode: &exitCode, SafeDetail: "container exited"}
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
	if got.State != core.WorkerFailed || got.FailureReason != "container exited (exit code 13)" {
		t.Fatalf("worker=%+v", got)
	}
	if got.ProviderExitCode == nil || *got.ProviderExitCode != 13 {
		t.Fatalf("provider exit code=%v, want 13", got.ProviderExitCode)
	}
}

func TestProviderFailureQueuesImmediateCleanup(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hub, err := agenthub.New(st, "enrollment-secret", "agent-secret-that-is-at-least-thirty-two-bytes")
	if err != nil {
		t.Fatal(err)
	}

	worker := core.Worker{
		ID:             "worker-cleanup",
		HostID:         "host-1",
		ProviderID:     "docker",
		ProviderHandle: "container-456",
		State:          core.WorkerRegistering,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st, Hub: hub}
	exitCode := 137
	providerState := provider.WorkerState{
		State:      core.WorkerFailed,
		ExitCode:   &exitCode,
		OOMKilled:  true,
		SafeDetail: "container exited; OOM killed",
	}
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
	if got.State != core.WorkerCleanupPending {
		t.Fatalf("state=%s, want cleanup_pending", got.State)
	}
	if !got.ProviderOOMKilled || got.ProviderExitCode == nil || *got.ProviderExitCode != 137 {
		t.Fatalf("provider diagnostics not retained: %+v", got)
	}

	cmds := hub.Next("host-1")
	if len(cmds) != 1 || cmds[0].Type != agent.CommandDelete {
		t.Fatalf("expected immediate delete command, got %+v", cmds)
	}
	if cmds[0].Handle == nil || cmds[0].Handle.ProviderID != worker.ProviderHandle {
		t.Fatalf("delete command handle=%+v", cmds[0].Handle)
	}
}
