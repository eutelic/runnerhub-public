package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/scheduler"
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
		ID:         "worker-create",
		HostID:     "host-1",
		ProviderID: "docker",
		State:      core.WorkerProvisioning,
		UpdatedAt:  time.Now().UTC(),
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

func TestProviderDisappearedAfterSuccessfulJobQueuesCleanup(t *testing.T) {
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
		ID:             "worker-successful-disappeared",
		HostID:         "host-1",
		ProviderID:     "docker",
		ProviderHandle: "container-successful-disappeared",
		State:          core.WorkerCompleted,
		StartedAt:      now.Add(-time.Minute),
		CompletedAt:    now.Add(-30 * time.Second),
		JobResult:      "succeeded",
		UpdatedAt:      now,
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st, Hub: hub}
	if err := svc.HandleAgentResult(ctx, "host-1", agent.CommandResult{
		WorkerID: worker.ID,
		OK:       true,
		State:    &provider.WorkerState{State: core.WorkerDeleted},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending {
		t.Fatalf("state=%s, want cleanup_pending so provider cleanup remains retryable", got.State)
	}
	if got.JobResult != "succeeded" || !got.FailedAt.IsZero() || got.FailureReason != "" {
		t.Fatalf("confirmed successful job was rewritten as failed: %+v", got)
	}
	if got.ProviderHandle != worker.ProviderHandle {
		t.Fatalf("provider handle=%q, want %q retained for idempotent cleanup", got.ProviderHandle, worker.ProviderHandle)
	}

	cmds := hub.Next("host-1")
	if len(cmds) != 1 || cmds[0].Type != agent.CommandDelete {
		t.Fatalf("expected cleanup delete command, got %+v", cmds)
	}
	if cmds[0].Handle == nil || cmds[0].Handle.ProviderID != worker.ProviderHandle {
		t.Fatalf("delete command handle=%+v", cmds[0].Handle)
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
		OK:       true,
		State: &provider.WorkerState{
			State: core.WorkerFailed, ExitCode: &exitCode,
			SafeDetail:  "container exited",
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
		OK:       true,
		State: &provider.WorkerState{
			State: core.WorkerFailed, ExitCode: &exitCode,
			SafeDetail:  "container exited",
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
func TestScaleCompletionBeforeStartedRequiresAndPersistsMatchingJobIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pool := core.RunnerPool{ID: "pool-early-completion", Name: "early-completion", Scope: core.GitHubScope{Kind: core.ScopeRepository, Owner: "acme", Repository: "app"}, Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}, MaxConcurrency: 1, RunnerGroupID: 1}
	if err := st.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	worker := core.Worker{ID: "worker-early-completion", PoolID: pool.ID, RunnerName: "runnerhub-abc", State: core.WorkerRegistering, Scope: pool.Scope, RegisteredAt: time.Now().UTC()}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", bytes.NewBufferString(
		`{"poolId":"pool-early-completion","runnerName":"runnerhub-abc","state":"completed","repository":"acme/app","workflowRef":"acme/app/.github/workflows/build.yml@main","workflowRunId":1234,"jobId":"5678","result":"success"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("scale completion status=%d body=%s", response.Code, response.Body.String())
	}
	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCompleted || got.JobID != "5678" || got.WorkflowRunID != 1234 ||
		got.Repository != "acme/app" || got.StartedAt.IsZero() == false || got.CompletedAt.IsZero() {
		t.Fatalf("early terminal event did not preserve truthful lifecycle identity: %+v", got)
	}
}

func TestScaleCompletionBeforeStartedRejectsMismatchedRepository(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pool := core.RunnerPool{ID: "pool-early-completion", Name: "early-completion", Scope: core.GitHubScope{Kind: core.ScopeRepository, Owner: "acme", Repository: "app"}, Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}, MaxConcurrency: 1, RunnerGroupID: 1}
	if err := st.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	worker := core.Worker{ID: "worker-early-completion", PoolID: pool.ID, RunnerName: "runnerhub-abc", State: core.WorkerRegistering, Scope: pool.Scope}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", bytes.NewBufferString(
		`{"poolId":"pool-early-completion","runnerName":"runnerhub-abc","state":"completed","repository":"acme/other","workflowRef":"acme/other/.github/workflows/build.yml@main","workflowRunId":1234,"jobId":"5678","result":"success"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("mismatched completion status=%d body=%s, want 409", response.Code, response.Body.String())
	}
	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerRegistering || got.JobID != "" || !got.CompletedAt.IsZero() {
		t.Fatalf("rejected completion mutated worker: %+v", got)
	}
}

func TestCanceledPublisherExitQueuesExactCleanupAndHoldsReservationUntilDelete(t *testing.T) {
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

	host := core.Host{
		ID: "host-1", Name: "publisher-host", Provider: "docker", State: core.HostOnline,
		Tags:     map[string]string{"isolation.container-dind": "true"},
		Capacity: core.Resources{CPUMillis: 10000, MemoryBytes: 32 << 30, DiskBytes: 40 << 30},
	}
	if err := st.UpsertHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	pool := core.RunnerPool{
		ID: "publisher", Name: "publisher", Isolation: core.IsolationContainerDinD,
		Resources:      core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 1 << 30},
		MaxConcurrency: 2,
		Scope:          core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
	}
	if err := st.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	worker := core.Worker{
		ID: "worker-canceled-publisher", HostID: host.ID, PoolID: pool.ID,
		RunnerName: "runnerhub-publisher-1", ProviderID: "docker",
		ProviderHandle: "runner-container-1", Isolation: core.IsolationContainerDinD,
		Resources: pool.Resources, State: core.WorkerBusy,
		Scope: pool.Scope, JobName: "publish (agent, runnerhub-agent)",
		WorkflowRunID: 1234, JobID: "5678", StartedAt: time.Now().UTC().Add(-10 * time.Minute),
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st, Hub: hub}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", bytes.NewBufferString(`{"poolId":"publisher","runnerName":"runnerhub-publisher-1","state":"completed","result":"Canceled","workflowRunId":1234,"jobId":"5678","jobName":"publish (agent, runnerhub-agent)"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("scale completion status=%d body=%s", response.Code, response.Body.String())
	}

	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending || got.JobResult != "Canceled" {
		t.Fatalf("canceled scale-set completion state/result=%s/%q, want cleanup_pending/Canceled", got.State, got.JobResult)
	}
	summary := scheduler.SummarizeCapacity(pool, []core.Host{host}, []core.Worker{got})
	if summary.Reserved.CPUMillis != 5000 || summary.Reserved.MemoryBytes != 16<<30 {
		t.Fatalf("canceled job released host reservation before provider delete: %+v", summary)
	}

	commands := hub.Next(host.ID)
	if len(commands) != 1 || commands[0].Type != agent.CommandDelete {
		t.Fatalf("expected exact-owned runner delete after cancellation, got %+v", commands)
	}
	if commands[0].WorkerID != worker.ID {
		t.Fatalf("delete command worker=%q, want %q", commands[0].WorkerID, worker.ID)
	}
	if commands[0].Handle == nil || commands[0].Handle.HostID != host.ID || commands[0].Handle.ProviderID != worker.ProviderHandle {
		t.Fatalf("delete handle=%+v, want host=%s runner=%s", commands[0].Handle, host.ID, worker.ProviderHandle)
	}

	exitCode := 143
	providerState := provider.WorkerState{State: core.WorkerFailed, ExitCode: &exitCode, SafeDetail: "container exited"}
	if err := svc.HandleAgentResult(ctx, host.ID, agent.CommandResult{WorkerID: worker.ID, OK: true, State: &providerState}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending || got.JobResult != "Canceled" || got.ProviderExitCode == nil || *got.ProviderExitCode != 143 {
		t.Fatalf("runner exit overwrote cancellation or cleanup state: %+v", got)
	}
	summary = scheduler.SummarizeCapacity(pool, []core.Host{host}, []core.Worker{got})
	if summary.Reserved.CPUMillis != 5000 || summary.Reserved.MemoryBytes != 16<<30 {
		t.Fatalf("reservation released before delete completed: %+v", summary)
	}

	if err := svc.HandleAgentResult(ctx, host.ID, agent.CommandResult{CommandID: commands[0].ID, WorkerID: worker.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerDeleted || got.JobResult != "Canceled" {
		t.Fatalf("successful delete did not finish canceled worker cleanly: %+v", got)
	}
	summary = scheduler.SummarizeCapacity(pool, []core.Host{host}, []core.Worker{got})
	if summary.Reserved.CPUMillis != 0 || summary.Reserved.MemoryBytes != 0 || summary.AdditionalWorkers != 2 {
		t.Fatalf("reservation did not release after delete: %+v", summary)
	}
}
