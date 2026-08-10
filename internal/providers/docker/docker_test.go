package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
)

type fakeRunner struct {
	calls [][]string
	fn    func(string, ...string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, n string, a ...string) (string, error) {
	f.calls = append(f.calls, append([]string{n}, a...))
	if f.fn != nil {
		return f.fn(n, a...)
	}
	return "", nil
}

func TestCreateHasNoPortsOrDockerSocketHidesJITAndPullsImage(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "run" {
			return "abc123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"ghcr.io/actions/actions-runner:latest"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	s := provider.WorkerSpec{
		WorkerID:   "w1",
		PoolID:     "p1",
		RunnerName: "r1",
		JITConfig:  "SECRET-JIT",
		Image:      "ghcr.io/actions/actions-runner:latest",
		Isolation:  core.IsolationContainerTrusted,
		Resources:  core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
	}
	h, err := p.Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != "abc123" {
		t.Fatalf("handle=%+v", h)
	}
	joined := strings.Join(f.calls[0], " ")
	if strings.Contains(joined, "SECRET-JIT") || strings.Contains(joined, "docker.sock") || strings.Contains(joined, " -p ") {
		t.Fatalf("unsafe docker args: %s", joined)
	}
	if !strings.Contains(joined, "/run/runnerhub/jitconfig") {
		t.Fatalf("missing jit secret mount: %s", joined)
	}
	if !strings.Contains(joined, "--pull always") {
		t.Fatalf("runner image is not refreshed by default: %s", joined)
	}
}

func TestCreateAllowsExplicitPullPolicy(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "run" {
			return "abc123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}, PullPolicy: "never"}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit", Image: "img", Isolation: core.IsolationContainerTrusted})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[0], " "); !strings.Contains(got, "--pull never") {
		t.Fatalf("explicit pull policy not used: %s", got)
	}
}

func TestRejectsInvalidPullPolicy(t *testing.T) {
	if _, err := newWithRunner(Config{PullPolicy: "sometimes"}, &fakeRunner{}); err == nil {
		t.Fatal("invalid Docker pull policy accepted")
	}
}

func TestCreateRecoversExistingContainerAfterAmbiguousRunFailure(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "run" {
			return "", errors.New("name conflict")
		}
		if len(a) > 0 && a[0] == "ps" {
			return "existing123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit", Image: "img", Isolation: core.IsolationContainerTrusted})
	if err != nil || h.ProviderID != "existing123" {
		t.Fatalf("handle=%+v err=%v", h, err)
	}
}
