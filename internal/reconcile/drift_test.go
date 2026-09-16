package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type missingGH struct{}

func (missingGH) GenerateJITConfig(context.Context, githubrunner.JITConfigRequest) (githubrunner.JITConfig, error) {
	return githubrunner.JITConfig{}, nil
}

func (missingGH) GetRunner(context.Context, core.GitHubScope, int64) (githubrunner.Runner, error) {
	return githubrunner.Runner{}, githubrunner.ErrNotFound
}

func (missingGH) DeleteRunner(context.Context, core.GitHubScope, int64) error {
	return githubrunner.ErrNotFound
}


type busyGH struct {
	runner githubrunner.Runner
}

func (b busyGH) GenerateJITConfig(context.Context, githubrunner.JITConfigRequest) (githubrunner.JITConfig, error) {
	return githubrunner.JITConfig{}, nil
}

func (b busyGH) GetRunner(context.Context, core.GitHubScope, int64) (githubrunner.Runner, error) {
	return b.runner, nil
}

func (b busyGH) DeleteRunner(context.Context, core.GitHubScope, int64) error {
	return nil
}

func TestRegisteringRunnerObservedBusyAdvancesThroughReady(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:                    "w-immediate",
		RunnerName:            "runner-immediate",
		GitHubRunnerID:        42,
		State:                 core.WorkerRegistering,
		RegistrationStartedAt: now.Add(-10 * time.Second),
		UpdatedAt:             now.Add(-time.Second),
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{
		Store: st,
		GitHub: busyGH{runner: githubrunner.Runner{
			ID: 42, Name: "runner-immediate", Status: "online", Busy: true,
			Ephemeral: true, Version: "2.337.0",
		}},
		Now: func() time.Time { return now },
	}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerBusy {
		t.Fatalf("state=%s, want busy", got.State)
	}
	if got.RegisteredAt.IsZero() || got.StartedAt.IsZero() {
		t.Fatalf("immediate assignment did not record registration/start timestamps: %+v", got)
	}
	if got.GitHubRunnerVersion != "2.337.0" || !got.GitHubRunnerEphemeral {
		t.Fatalf("runner metadata not retained: %+v", got)
	}
}

func TestMissingGitHubRunnerFailsDirectWorkerAfterGrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:             "w",
		RunnerName:     "runner-w",
		GitHubRunnerID: 7,
		State:          core.WorkerReady,
		UpdatedAt:      now.Add(-2 * time.Minute),
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{Store: st, GitHub: missingGH{}, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerDeleted {
		t.Fatalf("missing GitHub registration was not driven through cleanup, state=%s", got.State)
	}
}

func TestRegisteringRunnerGetsShortNotFoundGrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:             "w",
		RunnerName:     "runner-w",
		GitHubRunnerID: 7,
		State:          core.WorkerRegistering,
		UpdatedAt:      now.Add(-30 * time.Second),
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{Store: st, GitHub: missingGH{}, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerRegistering {
		t.Fatalf("registration grace not honored, state=%s", got.State)
	}
}


func TestRegisteringRunnerGraceUsesStableRegistrationStart(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:                    "w-stable-grace",
		RunnerName:            "runner-w",
		GitHubRunnerID:        8,
		State:                 core.WorkerRegistering,
		RegistrationStartedAt: now.Add(-2 * time.Minute),
		UpdatedAt:             now,
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{Store: st, GitHub: missingGH{}, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerDeleted {
		t.Fatalf("fresh UpdatedAt incorrectly extended registration grace, state=%s", got.State)
	}
}


func TestBusyStartedRunnerMayDisappearFromGitHubBeforeProviderExit(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:             "w-ephemeral-finished",
		RunnerName:     "runner-ephemeral",
		GitHubRunnerID: 99,
		State:          core.WorkerBusy,
		RegisteredAt:   now.Add(-time.Minute),
		StartedAt:      now.Add(-45 * time.Second),
		UpdatedAt:      now.Add(-time.Second),
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{Store: st, GitHub: missingGH{}, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerBusy {
		t.Fatalf("post-start ephemeral disappearance was treated as failure, state=%s reason=%q", got.State, got.FailureReason)
	}
	if !got.FailedAt.IsZero() || got.FailureReason != "" {
		t.Fatalf("post-start disappearance recorded failure metadata: %+v", got)
	}
}

func TestProviderBackedCleanupWaitsForAgentHub(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	w := core.Worker{
		ID:             "w",
		ProviderID:     "docker",
		ProviderHandle: "runnerhub-w",
		State:          core.WorkerFailed,
		UpdatedAt:      now.Add(-time.Minute),
	}
	if err := st.UpsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	r := &Reconciler{Store: st, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending {
		t.Fatalf("provider-backed worker should remain cleanup-pending without an agent hub, state=%s", got.State)
	}
}
