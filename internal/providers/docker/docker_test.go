package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/provider"
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
	if !strings.Contains(joined, "/home/runner/_work:rw,exec,nosuid,nodev,mode=0770,uid=1001,gid=1001") {
		t.Fatalf("trusted runner workdir tmpfs is not explicitly owned by runner uid/gid: %s", joined)
	}
}

func TestCreateExtractsContainerIDFromCombinedPullOutput(t *testing.T) {
	const id = "d860df4b25b214321046b596fff7b6008195b1af11d9c1426f98af5ec2448e34"
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "run" {
			return "layer: Pulling fs layer\nDigest: sha256:deadbeef\nStatus: Downloaded newer image\n" + id + "\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit",
		Image: "img", Isolation: core.IsolationContainerTrusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != id {
		t.Fatalf("provider handle=%q, want exact container id %q", h.ProviderID, id)
	}
}

func TestCreateFallsBackToExactNameWhenRunOutputIsAmbiguous(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if len(a) > 0 && a[0] == "run" {
			return "Pulling image\nwarning from docker\n", nil
		}
		if strings.HasPrefix(joined, "inspect runnerhub-w1 --format") {
			return id + "\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit",
		Image: "img", Isolation: core.IsolationContainerTrusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != id {
		t.Fatalf("provider handle=%q, want exact inspected id %q", h.ProviderID, id)
	}
}

func TestCreateDinDUsesPrivateSidecarSocketNotHostSocket(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--entrypoint /bin/bash") {
			return "1001:1001:123", nil
		}
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1-dind") {
			return "dind123\n", nil
		}
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1") {
			return "runner123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}, DinDImage: images.DefaultDinDImage, AllowPrivilegedWorkers: true}, f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit", Image: "img", Isolation: core.IsolationContainerDinD, Resources: core.Resources{CPUMillis: 2500, MemoryBytes: 8 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != "runner123" {
		t.Fatalf("handle=%+v", h)
	}
	all := make([]string, 0, len(f.calls))
	preflightSeen := false
	for _, call := range f.calls {
		joinedCall := strings.Join(call, " ")
		all = append(all, joinedCall)
		if strings.Contains(joinedCall, "test -r /run/runnerhub/jitconfig") {
			preflightSeen = true
			if !strings.Contains(joinedCall, "dst=/run/runnerhub-docker") {
				t.Fatalf("JIT preflight did not use isolated socket directory: %s", joinedCall)
			}
			if !strings.Contains(joinedCall, "--group-add 123") ||
				!strings.Contains(joinedCall, "touch /run/runnerhub-docker/.runnerhub-access-test") {
				t.Fatalf("JIT/socket-directory preflight did not run as the Docker-capable runner identity: %s", joinedCall)
			}
		}
	}
	joined := strings.Join(all, "\n")
	if !preflightSeen {
		t.Fatalf("DinD JIT readability preflight missing: %s", joined)
	}
	if strings.Contains(joined, "src=/var/run/docker.sock") || strings.Contains(joined, "-v /var/run/docker.sock") {
		t.Fatalf("host Docker socket leaked into worker path: %s", joined)
	}
	if strings.Contains(joined, "dst=/var/run") {
		t.Fatalf("DinD socket mount aliases the JIT /run tree through /var/run: %s", joined)
	}
	if !strings.Contains(joined, "DOCKER_HOST=unix:///run/runnerhub-docker/docker.sock") || !strings.Contains(joined, "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS=120") {
		t.Fatalf("runner is not wired to private Docker daemon: %s", joined)
	}
	if !strings.Contains(joined, "dst=/run/runnerhub-docker") {
		t.Fatalf("private DinD socket directory missing: %s", joined)
	}
	if !strings.Contains(joined, "--name runnerhub-w1-dind") ||
		!strings.Contains(joined, "--privileged") ||
		!strings.Contains(joined, "--network container:runner123") ||
		!strings.Contains(joined, images.DefaultDinDImage+" dockerd --host=unix:///run/runnerhub-docker/docker.sock") ||
		!strings.Contains(joined, "--group=123") {
		t.Fatalf("Docker-in-Docker sidecar missing required configuration: %s", joined)
	}
	if !strings.Contains(joined, "--group-add 123") {
		t.Fatalf("runner did not receive Docker socket supplementary gid: %s", joined)
	}
	if strings.Count(joined, "--cpus 2.500") < 2 || strings.Count(joined, "--memory 8589934592") < 2 {
		t.Fatalf("runner and DinD sidecar did not both receive accounted CPU/RAM ceilings: %s", joined)
	}
	if strings.Contains(joined, "--group-add 1001") || strings.Contains(joined, "--group=1001") {
		t.Fatalf("runner primary gid was incorrectly used as Docker socket group: %s", joined)
	}
	if !strings.Contains(joined, "while [ ! -f /run/runnerhub-docker/ready ]") ||
		!strings.Contains(joined, "docker info >/dev/null 2>&1") ||
		!strings.Contains(joined, "exec /home/runner/run.sh --jitconfig") {
		t.Fatalf("runner process does not gate GitHub listener startup on DinD readiness: %s", joined)
	}
	if strings.Contains(joined, " exec --env DOCKER_HOST=unix:///run/runnerhub-docker/docker.sock ") {
		t.Fatalf("DinD readiness still relies on a separate docker exec probe: %s", joined)
	}
	if _, err := os.Stat(filepath.Join(p.cfg.StateDir, "workers", "w1", "dind-run", "ready")); err != nil {
		t.Fatalf("DinD ready gate was not released after socket preflight: %v", err)
	}

	jitPath := filepath.Join(p.cfg.StateDir, "workers", "w1", "jitconfig")
	info, err := os.Stat(jitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 && os.Geteuid() == 0 {
		t.Fatalf("DinD JIT secret mode=%04o, want 0400", got)
	}
}

func TestCreateDinDFailsFastWhenRunnerCannotReadJITMount(t *testing.T) {
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--entrypoint /bin/bash") {
			return "1001:1001:123", nil
		}
		if strings.Contains(joined, "test -r /run/runnerhub/jitconfig") {
			return "", errors.New("permission denied")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"img"}, DinDImage: images.DefaultDinDImage, AllowPrivilegedWorkers: true}, f)
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit",
		Image: "img", Isolation: core.IsolationContainerDinD,
	})
	if err == nil || !strings.Contains(err.Error(), "preflight Docker-in-Docker JIT mount") {
		t.Fatalf("expected JIT preflight failure, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w1")); !os.IsNotExist(statErr) {
		t.Fatalf("failed DinD preflight left worker state behind: stat err=%v", statErr)
	}
}

func TestCreateDinDUsesRunnerProcessForSocketReadiness(t *testing.T) {
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		switch {
		case len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--entrypoint /bin/bash"):
			return "1001:1001:123", nil
		case len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1-dind"):
			return "dind123\n", nil
		case len(a) > 0 && a[0] == "run" && strings.Contains(joined, "--name runnerhub-w1"):
			return "runner123\n", nil
		case len(a) > 0 && a[0] == "exec":
			return "", errors.New("docker exec readiness probe must not be used")
		default:
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{
		StateDir: stateDir, AllowedImages: []string{"img"}, DinDImage: images.DefaultDinDImage, AllowPrivilegedWorkers: true,
	}, f)
	if err != nil {
		t.Fatal(err)
	}

	h, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit",
		Image: "img", Isolation: core.IsolationContainerDinD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.ProviderID != "runner123" {
		t.Fatalf("provider handle=%q, want runner123", h.ProviderID)
	}

	joined := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		joined = append(joined, strings.Join(call, " "))
	}
	all := strings.Join(joined, "\n")
	if strings.Contains(all, "docker exec") || strings.Contains(all, " exec --env DOCKER_HOST=") {
		t.Fatalf("socket readiness unexpectedly used docker exec: %s", all)
	}
	for _, want := range []string{
		"while [ ! -f /run/runnerhub-docker/ready ]",
		"docker info >/dev/null 2>&1",
		"RunnerHub DinD readiness failed in runner process",
		"stat -c '%n mode=%a uid=%u gid=%g type=%F' /run/runnerhub-docker/docker.sock",
		"exec /home/runner/run.sh --jitconfig",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("runner-side readiness gate missing %q: %s", want, all)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "workers", "w1", "dind-run", "ready")); err != nil {
		t.Fatalf("DinD ready gate was not released after sidecar start: %v", err)
	}
}

func TestDinDRequiresHostPrivilegedGate(t *testing.T) {
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range caps.IsolationModes {
		if mode == core.IsolationContainerDinD {
			t.Fatal("Docker host advertised container-dind without privileged-worker opt-in")
		}
	}
	if caps.Features["dockerInDocker"] || caps.Features["containerActions"] {
		t.Fatalf("privileged Docker features advertised without host opt-in: %+v", caps.Features)
	}
	if caps.Labels["isolation.container-dind"] == "true" {
		t.Fatalf("container-dind label advertised without host opt-in: %+v", caps.Labels)
	}

	err = p.Validate(context.Background(), provider.WorkerSpec{WorkerID: "w1", PoolID: "p1", RunnerName: "r1", JITConfig: "jit", Image: "img", Isolation: core.IsolationContainerDinD})
	if err == nil || !strings.Contains(err.Error(), "RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true") {
		t.Fatalf("container-dind host gate err=%v", err)
	}
}

func TestPrivilegedDinDRejectsMovingImageTag(t *testing.T) {
	_, err := newWithRunner(Config{
		StateDir: t.TempDir(), AllowedImages: []string{"img"},
		DinDImage: "docker:dind", AllowPrivilegedWorkers: true,
	}, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("moving privileged DinD image tag accepted: %v", err)
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

// Image-declared anonymous volumes must be removed with the trusted worker.
func TestDeleteTrustedWorkerRemovesAnonymousVolumes(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if strings.HasPrefix(joined, "inspect runner123 --format") {
			return "w1\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), provider.WorkerHandle{ProviderID: "runner123"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range f.calls {
		if strings.Join(call[1:], " ") == "rm -f -v runner123" {
			found = true
		}
	}
	if !found {
		t.Fatalf("trusted worker removal did not reclaim anonymous volumes: %+v", f.calls)
	}
}

func TestDeleteRemovesDinDSidecarBeforeRunner(t *testing.T) {
	sidecarRemoved := false
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		switch {
		case strings.HasPrefix(joined, "inspect runner123 --format"):
			return "w1\n", nil
		case len(a) > 0 && a[0] == "ps" && strings.Contains(joined, "label=io.runnerhub.parent=runner123"):
			return "dind123\n", nil
		case joined == "rm -f -v dind123":
			sidecarRemoved = true
			return "", nil
		case joined == "rm -f -v runner123":
			if !sidecarRemoved {
				return "", errors.New("conflict: container shares runner network namespace")
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), provider.WorkerHandle{ProviderID: "runner123"}); err != nil {
		t.Fatal(err)
	}

	sidecarIndex, runnerIndex := -1, -1
	for i, call := range f.calls {
		joined := strings.Join(call[1:], " ")
		if joined == "rm -f -v dind123" {
			sidecarIndex = i
		}
		if joined == "rm -f -v runner123" {
			runnerIndex = i
		}
	}
	if sidecarIndex < 0 || runnerIndex < 0 || sidecarIndex >= runnerIndex {
		t.Fatalf("DinD cleanup order was not sidecar-before-runner: %+v", f.calls)
	}
}

func TestStopStopsDinDSidecarBeforeRunner(t *testing.T) {
	sidecarStopped := false
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		switch {
		case strings.HasPrefix(joined, "inspect runner123 --format"):
			return "w1\n", nil
		case len(a) > 0 && a[0] == "ps" && strings.Contains(joined, "label=io.runnerhub.parent=runner123"):
			return "dind123\n", nil
		case joined == "stop --time 10 dind123":
			sidecarStopped = true
			return "", nil
		case joined == "stop --time 10 runner123":
			if !sidecarStopped {
				return "", errors.New("runner stopped before dependent DinD sidecar")
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background(), provider.WorkerHandle{ProviderID: "runner123"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRecoversDinDSidecarByParentWithoutLocalWorkerState(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		if len(a) > 0 && a[0] == "inspect" {
			return "", errors.New("No such object")
		}
		if len(a) > 0 && a[0] == "ps" && strings.Contains(joined, "label=io.runnerhub.parent=runner123") {
			return "dind123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), provider.WorkerHandle{ProviderID: "runner123"}); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, call := range f.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	if !strings.Contains(joined, "label=io.runnerhub.parent=runner123") {
		t.Fatalf("sidecar parent lookup was not attempted: %s", joined)
	}
	if !strings.Contains(joined, "rm -f -v dind123") {
		t.Fatalf("recovered sidecar was not removed: %s", joined)
	}
}

func TestJITBindMountFileIsReadableByNonRootRunner(t *testing.T) {
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "run" {
			return "abc123\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w1",
		PoolID: "p1",
		RunnerName: "r1",
		JITConfig: "secret",
		Image: "img",
		Isolation: core.IsolationContainerTrusted,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(stateDir + "/workers/w1/jitconfig")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 && got != 0o444 {
		t.Fatalf("jitconfig mode=%o, want normalized 0400 or non-root fallback 0444", got)
	}
}

func TestInspectFailedContainerCapturesRedactedDiagnostics(t *testing.T) {
	const secret = "github_pat_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		switch {
		case strings.HasPrefix(joined, "inspect runner123 --format"):
			return `{"Running":false,"ExitCode":1,"Status":"exited","OOMKilled":false,"Error":""}`, nil
		case strings.HasPrefix(joined, "logs --tail 80 runner123"):
			return "Listening for Jobs\nAuthorization: Bearer super-secret-value\ntoken=" + secret + "\n", nil
		default:
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Inspect(context.Background(), provider.WorkerHandle{ProviderID: "runner123"})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != core.WorkerFailed || st.ExitCode == nil || *st.ExitCode != 1 {
		t.Fatalf("state=%+v", st)
	}
	if !strings.Contains(st.Diagnostics, "Listening for Jobs") {
		t.Fatalf("diagnostics=%q", st.Diagnostics)
	}
	for _, secretValue := range []string{"super-secret-value", secret} {
		if strings.Contains(st.Diagnostics, secretValue) {
			t.Fatalf("diagnostics leaked secret %q: %q", secretValue, st.Diagnostics)
		}
	}
	if !strings.Contains(st.Diagnostics, "[REDACTED]") {
		t.Fatalf("diagnostics were not visibly redacted: %q", st.Diagnostics)
	}
}

func TestSanitizeDiagnosticsRedactsVercelToken(t *testing.T) {
	const token = "vcp_abcdefghijklmnopqrstuvwxyz012345"
	got := sanitizeDiagnostics("startup failed token=" + token + "\nraw " + token)
	if strings.Contains(got, token) {
		t.Fatalf("vercel token leaked from diagnostics: %q", got)
	}
	if !strings.Contains(got, "vcp_[REDACTED]") {
		t.Fatalf("vercel token redaction marker missing: %q", got)
	}
}

func TestSanitizeDiagnosticsBoundsOutputAndRedactsJWT(t *testing.T) {
	jwt := "eyJaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.cccccccccccccccccccccccccccccccc"
	input := jwt + "\n" + strings.Repeat("x ", maxDiagnosticsBytes)
	got := sanitizeDiagnostics(input)
	if strings.Contains(got, jwt) {
		t.Fatalf("jwt was not redacted")
	}
	if len(got) > maxDiagnosticsBytes+32 {
		t.Fatalf("diagnostics len=%d exceeds bounded size", len(got))
	}
	if !strings.HasPrefix(got, "[truncated]\n") {
		t.Fatalf("expected truncation marker, got prefix %q", got[:min(20, len(got))])
	}
}

func TestSanitizeDiagnosticsStopsBeforeWorkflowOutput(t *testing.T) {
	got := sanitizeDiagnostics("Current runner version: 2.337.0\nListening for Jobs\nRunning job: build\nPLAINTEXT_WORKFLOW_SECRET\n")
	if !strings.Contains(got, "Listening for Jobs") {
		t.Fatalf("startup evidence missing: %q", got)
	}
	if strings.Contains(got, "PLAINTEXT_WORKFLOW_SECRET") {
		t.Fatalf("workflow output leaked into startup diagnostics: %q", got)
	}
	if !strings.Contains(got, "[job output omitted]") {
		t.Fatalf("job boundary marker missing: %q", got)
	}
}


func TestInspectReportsCombinedRunnerAndDinDUsage(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		joined := strings.Join(a, " ")
		switch {
		case strings.HasPrefix(joined, "inspect runner123 --format"):
			return `{"Running":true,"ExitCode":0,"Status":"running"}`, nil
		case len(a) > 0 && a[0] == "ps":
			return "dind123\n", nil
		case strings.HasPrefix(joined, "inspect dind123 --format"):
			return `{"Running":true,"ExitCode":0,"Status":"running"}`, nil
		case strings.HasPrefix(joined, "stats --no-stream --format"):
			return "125.5%|512MiB / 8GiB\n50%|1GiB / 8GiB\n", nil
		default:
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Inspect(context.Background(), provider.WorkerHandle{ProviderID: "runner123"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Usage == nil {
		t.Fatal("expected runtime usage")
	}
	if st.Usage.CPUMillis != 1755 {
		t.Fatalf("cpu=%d, want 1755 millicores", st.Usage.CPUMillis)
	}
	if st.Usage.MemoryBytes != (512<<20)+(1<<30) {
		t.Fatalf("memory=%d, want %d", st.Usage.MemoryBytes, int64((512<<20)+(1<<30)))
	}
}

func TestParseDockerBytes(t *testing.T) {
	tests := map[string]int64{
		"512MiB": 512 << 20,
		"1.5GiB": 3 << 29,
		"250MB": 250000000,
		"0B": 0,
	}
	for input, want := range tests {
		got, err := parseDockerBytes(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s=%d, want %d", input, got, want)
		}
	}
}


func TestContainerUsageReportsAggregateAndPerContainerEnvelope(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, a ...string) (string, error) {
		if len(a) > 0 && a[0] == "stats" {
			return "250.00%|1GiB / 8GiB\n180.00%|3GiB / 8GiB\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"img"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := p.containerUsage(context.Background(), "runner123", "dind123")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CPUMillis != 4300 || usage.MemoryBytes != 4<<30 {
		t.Fatalf("aggregate usage=%+v", usage)
	}
	if usage.EnvelopeCPUMillis != 2500 || usage.EnvelopeMemoryBytes != 3<<30 {
		t.Fatalf("per-container envelope=%+v", usage)
	}
}
