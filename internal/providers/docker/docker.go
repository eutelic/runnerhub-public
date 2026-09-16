package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/hostinfo"
	"github.com/eutelic/runnerhub/internal/provider"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s: %w: %s", name, err, sanitize(string(b)))
	}
	return string(b), nil
}

type Config struct {
	DockerBinary           string
	StateDir               string
	AllowedImages          []string
	HostID                 string
	DefaultWorkdirBytes    int64
	PullPolicy             string
	DinDImage              string
	AllowPrivilegedWorkers bool
}

type Provider struct {
	cfg     Config
	run     CommandRunner
	allowed map[string]struct{}
}

func New(cfg Config) (*Provider, error) { return newWithRunner(cfg, execRunner{}) }

func newWithRunner(cfg Config, r CommandRunner) (*Provider, error) {
	if cfg.DockerBinary == "" {
		cfg.DockerBinary = "docker"
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/runnerhub"
	}
	if cfg.DefaultWorkdirBytes <= 0 {
		cfg.DefaultWorkdirBytes = 20 << 30
	}
	if len(cfg.AllowedImages) == 0 {
		cfg.AllowedImages = []string{"ghcr.io/actions/actions-runner:latest"}
	}
	if cfg.PullPolicy == "" {
		cfg.PullPolicy = "always"
	}
	if cfg.DinDImage == "" {
		cfg.DinDImage = "docker:dind"
	}
	switch cfg.PullPolicy {
	case "always", "missing", "never":
	default:
		return nil, fmt.Errorf("unsupported Docker pull policy %q; expected always, missing, or never", cfg.PullPolicy)
	}
	a := map[string]struct{}{}
	for _, i := range cfg.AllowedImages {
		a[i] = struct{}{}
	}
	return &Provider{cfg: cfg, run: r, allowed: a}, nil
}

func (p *Provider) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	if _, err := p.run.Run(ctx, p.cfg.DockerBinary, "version", "--format", "{{.Server.Version}}"); err != nil {
		return provider.Capabilities{}, err
	}
	isolationModes := []core.IsolationMode{core.IsolationContainerTrusted}
	features := map[string]bool{
		"ephemeral":        true,
		"publishedPorts":   false,
		"dockerSocket":     false,
		"dockerInDocker":   false,
		"containerActions": false,
	}
	labels := map[string]string{
		"isolation.container-trusted": "true",
	}
	if p.cfg.AllowPrivilegedWorkers {
		isolationModes = append(isolationModes, core.IsolationContainerDinD)
		features["dockerInDocker"] = true
		features["containerActions"] = true
		labels["isolation.container-dind"] = "true"
	}
	return provider.Capabilities{
		Provider:       "docker",
		OS:             runtime.GOOS,
		Architecture:   runtime.GOARCH,
		Capacity:       hostinfo.Capacity(p.cfg.StateDir),
		IsolationModes: isolationModes,
		Features:       features,
		Labels:         labels,
	}, nil
}

func (p *Provider) Validate(_ context.Context, s provider.WorkerSpec) error {
	if strings.TrimSpace(s.WorkerID) == "" || strings.TrimSpace(s.PoolID) == "" || strings.TrimSpace(s.RunnerName) == "" || s.JITConfig == "" {
		return errors.New("worker id, pool id, runner name and jit config are required")
	}
	switch s.Isolation {
	case core.IsolationContainerTrusted:
	case core.IsolationContainerDinD:
		if !p.cfg.AllowPrivilegedWorkers {
			return errors.New("container-dind requires host RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true")
		}
	default:
		return fmt.Errorf("docker provider supports %s and %s", core.IsolationContainerTrusted, core.IsolationContainerDinD)
	}
	if _, ok := p.allowed[s.Image]; !ok {
		return fmt.Errorf("image %q is not allowlisted", s.Image)
	}
	if !core.NonNegative(s.Resources) {
		return errors.New("resource requests cannot be negative")
	}
	return nil
}

func (p *Provider) Create(ctx context.Context, s provider.WorkerSpec) (provider.WorkerHandle, error) {
	if s.Image == "" {
		s.Image = "ghcr.io/actions/actions-runner:latest"
	}
	if err := p.Validate(ctx, s); err != nil {
		return provider.WorkerHandle{}, err
	}
	secretDir := filepath.Join(p.cfg.StateDir, "workers", safeID(s.WorkerID))
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		return provider.WorkerHandle{}, err
	}
	jitPath := filepath.Join(secretDir, "jitconfig")
	// The official runner image runs as UID 1001. The host-only 0700 parent
	// protects the secret at rest; the bind-mounted file itself must be readable
	// by the non-root process inside the worker container.
	if err := os.WriteFile(jitPath, []byte(s.JITConfig), 0o444); err != nil {
		return provider.WorkerHandle{}, err
	}

	var id string
	var err error
	if s.Isolation == core.IsolationContainerDinD {
		id, err = p.createDinDWorker(ctx, s, secretDir, jitPath)
	} else {
		id, err = p.createTrustedWorker(ctx, s, secretDir, jitPath)
	}
	if err != nil {
		return provider.WorkerHandle{}, err
	}
	if err := os.WriteFile(filepath.Join(secretDir, "provider-id"), []byte(id), 0o600); err != nil {
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", id)
		if s.Isolation == core.IsolationContainerDinD {
			_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", workerContainerName(s.WorkerID)+"-dind")
		}
		_ = os.RemoveAll(secretDir)
		return provider.WorkerHandle{}, err
	}
	return provider.WorkerHandle{ProviderID: id, HostID: p.cfg.HostID}, nil
}

func (p *Provider) createTrustedWorker(ctx context.Context, s provider.WorkerSpec, secretDir, jitPath string) (string, error) {
	name := workerContainerName(s.WorkerID)
	args := p.runnerArgs(s, name, jitPath)
	size := s.Resources.DiskBytes
	if size <= 0 {
		size = p.cfg.DefaultWorkdirBytes
	}
	args = append(args,
		"--tmpfs", "/home/runner/_work:rw,exec,nosuid,nodev,size="+strconv.FormatInt(size, 10),
		s.Image,
		"/bin/bash", "-lc", `exec /home/runner/run.sh --jitconfig "$(cat /run/runnerhub/jitconfig)"`,
	)
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, args...)
	if err != nil {
		if id, findErr := p.findExact(ctx, name); findErr == nil && id != "" {
			return id, nil
		}
		_ = os.RemoveAll(secretDir)
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		_ = os.RemoveAll(secretDir)
		return "", errors.New("docker returned empty container id")
	}
	return id, nil
}

func (p *Provider) createDinDWorker(ctx context.Context, s provider.WorkerSpec, secretDir, jitPath string) (string, error) {
	workDir := filepath.Join(secretDir, "work")
	runDir := filepath.Join(secretDir, "dind-run")
	externalsDir := filepath.Join(secretDir, "dind-externals")
	for _, dir := range []string{workDir, runDir, externalsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = os.RemoveAll(secretDir)
			return "", err
		}
	}

	// The inner Docker daemon must see the same work and runner-externals paths
	// that the runner passes when launching container actions. Initialize those
	// directories using the selected runner image and capture its runner GID so
	// dockerd can create a socket the non-root runner can use.
	initScript := `set -eu; uid="$(id -u runner)"; gid="$(id -g runner)"; chown "$uid:$gid" /home/runner/_work; cp -a /home/runner/externals/. /runnerhub-externals/; printf '%s' "$gid"`
	gidOut, err := p.run.Run(ctx, p.cfg.DockerBinary,
		"run", "--rm", "--pull", p.cfg.PullPolicy, "--user", "0", "--entrypoint", "/bin/bash",
		"--mount", "type=bind,src="+workDir+",dst=/home/runner/_work",
		"--mount", "type=bind,src="+externalsDir+",dst=/runnerhub-externals",
		s.Image, "-lc", initScript,
	)
	if err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("initialize Docker-in-Docker worker paths: %w", err)
	}
	gidFields := strings.Fields(gidOut)
	if len(gidFields) == 0 {
		_ = os.RemoveAll(secretDir)
		return "", errors.New("runner image returned no runner group id")
	}
	runnerGID := gidFields[len(gidFields)-1]
	if _, err := strconv.Atoi(runnerGID); err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("runner image returned invalid runner group id %q", sanitize(runnerGID))
	}

	name := workerContainerName(s.WorkerID)
	runnerArgs := p.runnerArgs(s, name, jitPath)
	runnerArgs = append(runnerArgs,
		"--mount", "type=bind,src="+workDir+",dst=/home/runner/_work",
		"--mount", "type=bind,src="+runDir+",dst=/var/run",
		"--env", "DOCKER_HOST=unix:///var/run/docker.sock",
		"--env", "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS=120",
		s.Image,
		"/bin/bash", "-lc", `exec /home/runner/run.sh --jitconfig "$(cat /run/runnerhub/jitconfig)"`,
	)
	runnerOut, err := p.run.Run(ctx, p.cfg.DockerBinary, runnerArgs...)
	if err != nil {
		if existing, findErr := p.findExact(ctx, name); findErr == nil && existing != "" {
			runnerOut = existing
		} else {
			_ = os.RemoveAll(secretDir)
			return "", err
		}
	}
	runnerID := strings.TrimSpace(runnerOut)
	if runnerID == "" {
		_ = os.RemoveAll(secretDir)
		return "", errors.New("docker returned empty runner container id")
	}

	dindName := name + "-dind"
	dindArgs := []string{
		"run", "-d",
		"--pull", p.cfg.PullPolicy,
		"--name", dindName,
		"--label", "io.runnerhub.worker=" + s.WorkerID,
		"--label", "io.runnerhub.pool=" + s.PoolID,
		"--label", "io.runnerhub.role=dind",
		"--label", "io.runnerhub.parent=" + runnerID,
		"--privileged",
		"--mount", "type=bind,src=" + workDir + ",dst=/home/runner/_work",
		"--mount", "type=bind,src=" + runDir + ",dst=/var/run",
		"--mount", "type=bind,src=" + externalsDir + ",dst=/home/runner/externals,readonly",
		p.cfg.DinDImage,
		"dockerd", "--host=unix:///var/run/docker.sock", "--group=" + runnerGID,
	}
	if _, err := p.run.Run(ctx, p.cfg.DockerBinary, dindArgs...); err != nil {
		if existing, findErr := p.findExact(ctx, dindName); findErr != nil || existing == "" {
			_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", runnerID)
			_ = os.RemoveAll(secretDir)
			return "", fmt.Errorf("start Docker-in-Docker sidecar: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(secretDir, "dind-mode"), []byte(p.cfg.DinDImage), 0o600); err != nil {
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", dindName)
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", runnerID)
		_ = os.RemoveAll(secretDir)
		return "", err
	}
	return runnerID, nil
}

func (p *Provider) runnerArgs(s provider.WorkerSpec, name, jitPath string) []string {
	args := []string{
		"run", "-d",
		"--pull", p.cfg.PullPolicy,
		"--name", name,
		"--label", "io.runnerhub.worker=" + s.WorkerID,
		"--label", "io.runnerhub.pool=" + s.PoolID,
		"--label", "io.runnerhub.role=runner",
		"--security-opt", "no-new-privileges",
		"--mount", "type=bind,src=" + jitPath + ",dst=/run/runnerhub/jitconfig,readonly",
	}
	if s.Resources.CPUMillis > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(float64(s.Resources.CPUMillis)/1000, 'f', 3, 64))
	}
	if s.Resources.MemoryBytes > 0 {
		args = append(args, "--memory", strconv.FormatInt(s.Resources.MemoryBytes, 10))
	}
	return args
}

func workerContainerName(workerID string) string { return "runnerhub-" + safeID(workerID) }

func (p *Provider) findExact(ctx context.Context, name string) (string, error) {
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, "inspect", name, "--format", "{{.ID}}")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Provider) Inspect(ctx context.Context, h provider.WorkerHandle) (provider.WorkerState, error) {
	if strings.TrimSpace(h.ProviderID) == "" {
		return provider.WorkerState{}, errors.New("container id required")
	}
	state, err := p.inspectContainer(ctx, h.ProviderID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such") {
			return provider.WorkerState{State: core.WorkerDeleted, SafeDetail: "container absent"}, nil
		}
		return provider.WorkerState{}, err
	}
	if state.Running {
		sidecar, err := p.findSidecarByParent(ctx, h.ProviderID)
		if err != nil {
			return provider.WorkerState{}, err
		}
		if p.handleUsesDinD(h.ProviderID) || sidecar != "" {
			if sidecar == "" {
				return provider.WorkerState{State: core.WorkerFailed, SafeDetail: "Docker-in-Docker sidecar absent"}, nil
			}
			dindState, err := p.inspectContainer(ctx, sidecar)
			if err != nil {
				return provider.WorkerState{}, err
			}
			if !dindState.Running {
				code := dindState.ExitCode
				return provider.WorkerState{State: core.WorkerFailed, ExitCode: &code, SafeDetail: "Docker-in-Docker sidecar exited"}, nil
			}
		}
		return provider.WorkerState{State: core.WorkerReady, SafeDetail: "container running"}, nil
	}
	code := state.ExitCode
	if state.Status == "exited" && state.ExitCode == 0 {
		return provider.WorkerState{State: core.WorkerCompleted, ExitCode: &code, SafeDetail: "container exited successfully"}, nil
	}
	return provider.WorkerState{State: core.WorkerFailed, ExitCode: &code, SafeDetail: "container exited"}, nil
}

type containerState struct {
	Running  bool   `json:"Running"`
	ExitCode int    `json:"ExitCode"`
	Status   string `json:"Status"`
}

func (p *Provider) inspectContainer(ctx context.Context, id string) (containerState, error) {
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, "inspect", id, "--format", "{{json .State}}")
	if err != nil {
		return containerState{}, err
	}
	var st containerState
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		return containerState{}, err
	}
	return st, nil
}

func (p *Provider) handleUsesDinD(providerID string) bool {
	workerID := p.workerIDFromState(providerID)
	if workerID == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(p.cfg.StateDir, "workers", safeID(workerID), "dind-mode"))
	return err == nil
}

func (p *Provider) findSidecarByParent(ctx context.Context, providerID string) (string, error) {
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, "ps", "-a", "--filter", "label=io.runnerhub.parent="+providerID, "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	ids := strings.Fields(out)
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("multiple Docker-in-Docker sidecars claim runner %q", providerID)
	}
}

func (p *Provider) Stop(ctx context.Context, h provider.WorkerHandle) error {
	if h.ProviderID == "" {
		return nil
	}
	workerID := p.workerIDForHandle(ctx, h.ProviderID)
	sidecarID, err := p.findSidecarByParent(ctx, h.ProviderID)
	if err != nil {
		return fmt.Errorf("find Docker-in-Docker sidecar before stop: %w", err)
	}
	_, runnerErr := p.run.Run(ctx, p.cfg.DockerBinary, "stop", "--time", "10", h.ProviderID)
	if runnerErr != nil && strings.Contains(strings.ToLower(runnerErr.Error()), "no such") {
		runnerErr = nil
	}
	if sidecarID != "" {
		_, sidecarErr := p.run.Run(ctx, p.cfg.DockerBinary, "stop", "--time", "10", sidecarID)
		if sidecarErr != nil && !strings.Contains(strings.ToLower(sidecarErr.Error()), "no such") && runnerErr == nil {
			return sidecarErr
		}
	} else if workerID != "" {
		_, sidecarErr := p.run.Run(ctx, p.cfg.DockerBinary, "stop", "--time", "10", workerContainerName(workerID)+"-dind")
		if sidecarErr != nil && !strings.Contains(strings.ToLower(sidecarErr.Error()), "no such") && runnerErr == nil {
			return sidecarErr
		}
	}
	return runnerErr
}

func (p *Provider) Delete(ctx context.Context, h provider.WorkerHandle) error {
	workerID := p.workerIDForHandle(ctx, h.ProviderID)
	sidecarID := ""
	if h.ProviderID != "" {
		var err error
		sidecarID, err = p.findSidecarByParent(ctx, h.ProviderID)
		if err != nil {
			return fmt.Errorf("find Docker-in-Docker sidecar before delete: %w", err)
		}
		_, err = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", h.ProviderID)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such") {
			return err
		}
	}
	if sidecarID != "" {
		_, err := p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", sidecarID)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such") {
			return err
		}
	} else if workerID != "" {
		_, err := p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", workerContainerName(workerID)+"-dind")
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such") {
			return err
		}
	}
	if workerID != "" {
		_ = os.RemoveAll(filepath.Join(p.cfg.StateDir, "workers", safeID(workerID)))
	}
	return nil
}

func (p *Provider) workerIDForHandle(ctx context.Context, providerID string) string {
	if providerID == "" {
		return ""
	}
	if out, err := p.run.Run(ctx, p.cfg.DockerBinary, "inspect", providerID, "--format", `{{index .Config.Labels "io.runnerhub.worker"}}`); err == nil {
		if id := strings.TrimSpace(out); id != "" {
			return id
		}
	}
	return p.workerIDFromState(providerID)
}

func (p *Provider) workerIDFromState(providerID string) string {
	entries, err := os.ReadDir(filepath.Join(p.cfg.StateDir, "workers"))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.cfg.StateDir, "workers", entry.Name(), "provider-id"))
		if err == nil && strings.TrimSpace(string(b)) == providerID {
			return entry.Name()
		}
	}
	return ""
}

func safeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	v := strings.Trim(b.String(), "-")
	if len(v) > 48 {
		v = v[:48]
	}
	if v == "" {
		v = "worker"
	}
	return v
}

func sanitize(s string) string {
	if len(s) > 512 {
		s = s[:512]
	}
	return strings.ReplaceAll(s, "\n", " ")
}
