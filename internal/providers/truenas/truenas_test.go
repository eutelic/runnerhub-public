package truenas

import (
	"context"
	"os"
	"strings"
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

type captureRunner struct{ calls [][]string }

func (r *captureRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return "ok", nil
}

func TestTrustedCustomAppEnforcesCpuMemoryAndTmpfsLimits(t *testing.T) {
	root := t.TempDir()
	runner := &captureRunner{}
	p, err := newWithRunner(Config{Root: root, AllowedImages: []string{"img"}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "worker-1", PoolID: "pool-1", RunnerName: "runner-1",
		JITConfig: "secret", Image: "img", Isolation: core.IsolationContainerTrusted,
		Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) == 0 {
		t.Fatal("no midclt call captured")
	}
	joined := strings.Join(runner.calls[0], " ")
	for _, want := range []string{
		`"cpus":"2.500"`,
		`"mem_limit":"8589934592"`,
		`/home/runner/_work:rw,exec,nosuid,nodev,size=21474836480`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("custom app payload missing %q: %s", want, joined)
		}
	}
}
