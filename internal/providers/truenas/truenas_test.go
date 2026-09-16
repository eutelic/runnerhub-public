package truenas

import (
	"context"
	"os"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
)

type testRunner struct{}

func (testRunner) Run(_ context.Context, _ string, _ ...string) (string, error) {
	return "ok", nil
}

func TestProviderRequiresExplicitDatasetRoot(t *testing.T) {
	if _, err := newWithRunner(Config{}, nil); err == nil {
		t.Fatal("expected empty TrueNAS root to be rejected")
	}
}

func TestProviderRejectsRelativeDatasetRoot(t *testing.T) {
	if _, err := newWithRunner(Config{Root: "runnerhub"}, nil); err == nil {
		t.Fatal("expected relative TrueNAS root to be rejected")
	}
}

func TestJITBindMountFileIsReadableByNonRootRunner(t *testing.T) {
	root := t.TempDir()
	p, err := newWithRunner(Config{Root: root, AllowedImages: []string{"img"}}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID:  "worker-1",
		PoolID:    "pool-1",
		RunnerName:"runner-1",
		JITConfig: "secret",
		Image:     "img",
		Isolation: core.IsolationContainerTrusted,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root + "/workers/rh-worker-1/jitconfig")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("jitconfig mode=%o, want 444 so the image's non-root runner user can read the bind mount", got)
	}
}
