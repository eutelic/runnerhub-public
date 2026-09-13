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
