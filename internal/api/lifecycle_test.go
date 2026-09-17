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

func TestProviderCompletedDuringCleanupDoesNotOverwriteSuccessfulJob(t *testing.T) {
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

	now := time.Now().UTC()
	worker := core.Worker{
		ID:             "worker-clean-exit",
		HostID:         "host-1",
		ProviderID:     "docker",
		ProviderHandle: "container-clean-exit",
		State:          core.WorkerCleanupPending,
		StartedAt:      now.Add(-time.Minute),
		CompletedAt:    now.Add(-30 * time.Second),
		JobResult:      "succeeded",
		UpdatedAt:      now,
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	exitCode := 0
	svc := &Service{Store: st, Hub: hub}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: worker.ID,
		OK:       true,
		State: &provider.WorkerState{
			State:      core.WorkerCompleted,
			ExitCode:   &exitCode,
			SafeDetail: "container exited",
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending {
		t.Fatalf("clean provider exit during cleanup changed state to %s", got.State)
	}
	if !got.FailedAt.IsZero() || got.FailureReason != "" {
		t.Fatalf("successful job was rewritten as failure: %+v", got)
	}
	if got.ProviderExitCode == nil || *got.ProviderExitCode != 0 {
		t.Fatalf("provider exit code=%v, want 0", got.ProviderExitCode)
	}
	cmds := hub.Next("host-1")
	if len(cmds) != 1 || cmds[0].Type != agent.CommandDelete {
		t.Fatalf("expected cleanup delete command after late completion, got %+v", cmds)
	}
}

func TestProviderCompletedBeforeRunnableStillFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	worker := core.Worker{
		ID:             "worker-early-exit",
		HostID:         "host-1",
		ProviderID:     "docker",
		ProviderHandle: "container-early-exit",
		State:          core.WorkerRegistering,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	exitCode := 0
	svc := &Service{Store: st}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: worker.ID,
		OK:       true,
		State: &provider.WorkerState{
			State:      core.WorkerCompleted,
			ExitCode:   &exitCode,
			SafeDetail: "container exited",
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerFailed || got.FailureReason != "provider exited before worker reached a runnable state" {
		t.Fatalf("unexpected early-exit classification: %+v", got)
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

func TestProviderFailureRetainsOnlyPreJobDiagnostics(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	startup := core.Worker{
		ID: "startup-failure", HostID: "host-1", ProviderID: "docker",
		ProviderHandle: "container-startup", State: core.WorkerRegistering,
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, startup); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st}
	exitCode := 1
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: startup.ID,
		OK: true,
		State: &provider.WorkerState{
			State: core.WorkerFailed, ExitCode: &exitCode,
			SafeDetail: "container exited",
			Diagnostics: "Current runner version: 2.337.0\nregistration failed",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, startup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderDiagnostics == "" || got.FailedAt.IsZero() {
		t.Fatalf("startup diagnostics/timestamp not retained: %+v", got)
	}

	job := core.Worker{
		ID: "job-failure", HostID: "host-1", ProviderID: "docker",
		ProviderHandle: "container-job", State: core.WorkerBusy,
		StartedAt: time.Now().UTC().Add(-time.Minute),
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: job.ID,
		OK: true,
		State: &provider.WorkerState{
			State: core.WorkerFailed, ExitCode: &exitCode,
			SafeDetail: "container exited",
			Diagnostics: "workflow output must not be retained",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetWorker(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderDiagnostics != "" {
		t.Fatalf("post-job diagnostics unexpectedly retained: %q", got.ProviderDiagnostics)
	}
}
