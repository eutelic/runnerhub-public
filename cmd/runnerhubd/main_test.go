package main

import (
	"context"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestMigrateLegacyRunnerPools(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	legacy := core.RunnerPool{ID: "legacy", Name: "legacy", Scope: core.GitHubScope{Kind: core.ScopeRepository, Owner: "acme", Repository: "app"}, Labels: []string{"runnerhub"}, Isolation: core.IsolationContainerTrusted, Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}, MaxConcurrency: 1, RunnerGroupID: 1, Image: images.LegacyDefaultRunnerImage}
	custom := legacy
	custom.ID = "custom"
	custom.Name = "custom"
	custom.Image = "ghcr.io/acme/custom:latest"
	if err := st.UpsertPool(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPool(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{
		ID: "legacy-worker", PoolID: legacy.ID, ProviderID: "docker",
		State: core.WorkerBusy, Resources: legacy.Resources,
	}); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyRunnerPools(ctx, st); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetPool(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != images.DefaultRunnerImage {
		t.Fatalf("legacy image=%q, want %q", got.Image, images.DefaultRunnerImage)
	}
	got, err = st.GetPool(ctx, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != custom.Image {
		t.Fatalf("custom image unexpectedly changed to %q", got.Image)
	}
	worker, err := st.GetWorker(ctx, "legacy-worker")
	if err != nil {
		t.Fatal(err)
	}
	if worker.Isolation != core.IsolationContainerTrusted {
		t.Fatalf("legacy worker isolation=%q, want %q", worker.Isolation, core.IsolationContainerTrusted)
	}
}
