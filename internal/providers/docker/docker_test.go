package docker

import (
	"context"
	"errors"
	"os"
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
	s := provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "SECRET-JIT", Image: "ghcr.io/actions/actions-runner:latest", Isolation: core.IsolationContainerTrusted, Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}}
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
	if strings.Contains(joined, "--privileged") {
		t.Fatalf("trusted container unexpectedly privileged: %s", joined)
	}
}

func TestCreateDinDUsesPrivateSidecarSocketNotHostSocket(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--entrypoint /bin/bash") {
			return "123", nil
		}
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1-dind") {
			return "dind123\n", nil
		}
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1") {
			return "runner123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}, DinDImage: "docker:dind"}, f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit", Image: "img", Isolation: core.IsolationContainerDinD})
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != "runner123" {
		t.Fatalf("handle=%+v", h)
	}
	all := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		all = append(all, strings.Join(call, " "))
	}
	joined := strings.Join(all, "\n")
	if strings.Contains(joined, "src=/var/run/docker.sock") || strings.Contains(joined, "-v /var/run/docker.sock") {
		t.Fatalf("host Docker socket leaked into worker path: %s", joined)
	}
	if !strings.Contains(joined, "DOCKER_HOST=unix:///var/run/docker.sock") || !strings.Contains(joined, "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS=120") {
		t.Fatalf("runner is not wired to private Docker daemon: %s", joined)
	}
	if !strings.Contains(joined, "--name runnerhub-w1-dind") || !strings.Contains(joined, "--privileged") || !strings.Contains(joined, "docker:dind dockerd") || !strings.Contains(joined, "--group=123") {
		t.Fatalf("Docker-in-Docker sidecar missing required configuration: %s", joined)
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
		if len(a) > 1 && a[0] == "inspect" && a[1] == "runnerhub-w1" {
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

func TestDinDSidecarFailureMarksWorkerFailed(t *testing.T) {
	stateDir := t.TempDir()
	workerDir := stateDir + "/workers/w1"
	if err := os.MkdirAll(workerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerDir+"/provider-id", []byte("runner123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerDir+"/dind-mode", []byte("docker:dind"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if strings.HasPrefix(joined, "inspect runner123 --format") {
			return `{"Running":true,"ExitCode":0,"Status":"running"}`, nil
		}
		if len(a) > 0 && a[0] == "ps" {
			return "dind123\n", nil
		}
		if strings.HasPrefix(joined, "inspect dind123 --format") {
			return `{"Running":false,"ExitCode":1,"Status":"exited"}`, nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Inspect(context.Background(), provider.WorkerHandle{ProviderID: "runner123"})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != core.WorkerFailed || !strings.Contains(st.SafeDetail, "sidecar") {
		t.Fatalf("state=%+v", st)
	}
}
