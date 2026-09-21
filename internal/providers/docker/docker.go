package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/hostinfo"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/provider"
)

const (
	maxDiagnosticsBytes = 4096
	runnerUID           = 1001
	runnerGID           = 1001
	dindSocketDir       = "/run/runnerhub-docker"
	dindSocketPath      = dindSocketDir + "/docker.sock"
)

var detachedContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var dockerBytePattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?i?b)$`)

var diagnosticSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|token|basic)\s+)\S+`), "$1[REDACTED]"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "$1[REDACTED]"},
	{regexp.MustCompile(`(?i)(github_pat_|gh[pousr]_)[A-Za-z0-9_]{12,}`), "$1[REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\b`), "[REDACTED-JWT]"},
	{regexp.MustCompile(`(?i)((?:token|secret|password|jitconfig)\s*[=:]\s*)\S+`), "$1[REDACTED]"},
	{regexp.MustCompile(`[A-Za-z0-9_-]{96,}`), "[REDACTED-LONG-TOKEN]"},
}

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
		cfg.AllowedImages = []string{images.DefaultRunnerImage}
	}
	if cfg.PullPolicy == "" {
		cfg.PullPolicy = "always"
	}
	if cfg.DinDImage == "" {
		cfg.DinDImage = images.DefaultDinDImage
	}
	switch cfg.PullPolicy {
	case "always", "missing", "never":
	default:
		return nil, fmt.Errorf("unsupported Docker pull policy %q; expected always, missing, or never", cfg.PullPolicy)
	}
	if cfg.AllowPrivilegedWorkers {
		if err := images.ValidatePinned(cfg.DinDImage); err != nil {
			return nil, fmt.Errorf("Docker-in-Docker image must be digest-pinned when privileged workers are enabled: %w", err)
		}
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
		"resourceUsage":    true,
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

func secureJITSecretForRunner(path string, uid, gid int) error {
	if err := os.Chown(path, uid, gid); err != nil {
		// Keep unit tests and non-root developer environments usable. Production
		// RunnerHub images run the agent as root; a non-root caller cannot safely
		// change ownership, so retain a read-only fallback there.
		if os.Geteuid() != 0 {
			if chmodErr := os.Chmod(path, 0o444); chmodErr != nil {
				return fmt.Errorf("set JIT secret fallback mode after chown failure: %w", chmodErr)
			}
			return nil
		}
		return fmt.Errorf("set JIT secret owner to %d:%d: %w", uid, gid, err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return fmt.Errorf("set JIT secret mode: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify JIT secret: %w", err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		return fmt.Errorf("verify JIT secret mode: got %04o, want 0400", got)
	}
	return nil
}

func writeJITSecret(path string, data []byte) error {
	// The production Docker agent runs as root while the official Actions runner
	// image normally runs as UID/GID 1001. TrueNAS/ZFS ACL inheritance can narrow
	// create-mode bits, so explicitly hand ownership to the runner. DinD workers
	// re-normalize this ownership after asking the selected image for its actual
	// runner UID/GID.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return secureJITSecretForRunner(path, runnerUID, runnerGID)
}

func secureDinDSocketDir(path string, uid, gid int) error {
	if err := os.Chown(path, uid, gid); err != nil {
		// Unit tests and non-root development runners may not be allowed to
		// change ownership. The production agent runs as root; locally, keeping
		// the directory owner-accessible is sufficient for command-shape tests.
		if os.Geteuid() != 0 {
			if chmodErr := os.Chmod(path, 0o700); chmodErr != nil {
				return fmt.Errorf("set DinD socket directory fallback mode after chown failure: %w", chmodErr)
			}
			return nil
		}
		return fmt.Errorf("set DinD socket directory owner to %d:%d: %w", uid, gid, err)
	}
	if err := os.Chmod(path, 0o770); err != nil {
		return fmt.Errorf("set DinD socket directory mode: %w", err)
	}
	return nil
}

func (p *Provider) Create(ctx context.Context, s provider.WorkerSpec) (provider.WorkerHandle, error) {
	if s.Image == "" {
		s.Image = images.DefaultRunnerImage
	}
	if err := p.Validate(ctx, s); err != nil {
		return provider.WorkerHandle{}, err
	}
	secretDir := filepath.Join(p.cfg.StateDir, "workers", safeID(s.WorkerID))
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		return provider.WorkerHandle{}, err
	}
	jitPath := filepath.Join(secretDir, "jitconfig")
	if err := writeJITSecret(jitPath, []byte(s.JITConfig)); err != nil {
		_ = os.RemoveAll(secretDir)
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
		"--tmpfs", "/home/runner/_work:rw,exec,nosuid,nodev,mode=0770,uid=1001,gid=1001,size="+strconv.FormatInt(size, 10),
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
	id, resolveErr := p.resolveRunContainerID(ctx, name, out)
	if resolveErr != nil {
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", name)
		_ = os.RemoveAll(secretDir)
		return "", resolveErr
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
	// directories using the selected runner image and capture both the runner's
	// primary identity and the supplementary Docker group used for socket access.
	initScript := `set -eu
uid="$(id -u runner)"
gid="$(id -g runner)"
docker_gid="$(getent group docker | cut -d: -f3)"
test -n "$docker_gid"
case " $(id -G runner) " in
  *" $docker_gid "*) ;;
  *) echo "runner is not a member of docker gid $docker_gid" >&2; exit 1 ;;
esac
chown "$uid:$gid" /home/runner/_work
cp -a /home/runner/externals/. /runnerhub-externals/
printf '%s:%s:%s' "$uid" "$gid" "$docker_gid"`
	identityOut, err := p.run.Run(ctx, p.cfg.DockerBinary,
		"run", "--rm", "--pull", p.cfg.PullPolicy, "--user", "0", "--entrypoint", "/bin/bash",
		"--mount", "type=bind,src="+workDir+",dst=/home/runner/_work",
		"--mount", "type=bind,src="+externalsDir+",dst=/runnerhub-externals",
		s.Image, "-lc", initScript,
	)
	if err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("initialize Docker-in-Docker worker paths: %w", err)
	}
	identityFields := strings.Fields(identityOut)
	if len(identityFields) == 0 {
		_ = os.RemoveAll(secretDir)
		return "", errors.New("runner image returned no runner identity")
	}
	identity := strings.Split(identityFields[len(identityFields)-1], ":")
	if len(identity) != 3 {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("runner image returned invalid runner identity %q", sanitize(identityFields[len(identityFields)-1]))
	}
	runnerUID, err := strconv.Atoi(identity[0])
	if err != nil || runnerUID < 0 {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("runner image returned invalid runner uid %q", sanitize(identity[0]))
	}
	runnerPrimaryGID, err := strconv.Atoi(identity[1])
	if err != nil || runnerPrimaryGID < 0 {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("runner image returned invalid runner primary gid %q", sanitize(identity[1]))
	}
	runnerDockerGID, err := strconv.Atoi(identity[2])
	if err != nil || runnerDockerGID < 0 {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("runner image returned invalid docker gid %q", sanitize(identity[2]))
	}
	if err := secureJITSecretForRunner(jitPath, runnerUID, runnerPrimaryGID); err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("normalize Docker-in-Docker JIT secret ownership: %w", err)
	}
	if err := secureDinDSocketDir(runDir, runnerUID, runnerDockerGID); err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("normalize Docker-in-Docker socket directory: %w", err)
	}
	runnerDockerGroup := strconv.Itoa(runnerDockerGID)

	// Validate the exact mount topology as the image's default runner user before
	// starting the long-lived worker. This catches host ACL/user-namespace and
	// mount-shadowing problems without ever putting the JIT payload in argv.
	if _, err := p.run.Run(ctx, p.cfg.DockerBinary,
		"run", "--rm", "--pull", p.cfg.PullPolicy,
		"--group-add", runnerDockerGroup,
		"--mount", "type=bind,src="+jitPath+",dst=/run/runnerhub/jitconfig,readonly",
		"--mount", "type=bind,src="+runDir+",dst="+dindSocketDir,
		s.Image, "/bin/bash", "-lc",
		"test -r /run/runnerhub/jitconfig && test -s /run/runnerhub/jitconfig && cat /run/runnerhub/jitconfig >/dev/null && touch /run/runnerhub-docker/.runnerhub-access-test && rm /run/runnerhub-docker/.runnerhub-access-test",
	); err != nil {
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("preflight Docker-in-Docker JIT mount: %w", err)
	}

	name := workerContainerName(s.WorkerID)
	runnerArgs := p.runnerArgs(s, name, jitPath)
	runnerBootstrap := `set -eu
while [ ! -f /run/runnerhub-docker/ready ]; do sleep 0.1; done
attempts="${RUNNER_WAIT_FOR_DOCKER_IN_SECONDS:-120}"
i=0
while [ "$i" -lt "$attempts" ]; do
  if docker info >/dev/null 2>&1; then
    exec /home/runner/run.sh --jitconfig "$(cat /run/runnerhub/jitconfig)"
  fi
  i=$((i+1))
  sleep 1
done
echo "RunnerHub DinD readiness failed in runner process" >&2
id >&2 || true
ls -ldn /run/runnerhub-docker >&2 || true
stat -c '%n mode=%a uid=%u gid=%g type=%F' /run/runnerhub-docker/docker.sock >&2 || true
docker info >&2 || true
exit 1`
	runnerArgs = append(runnerArgs,
		"--group-add", runnerDockerGroup,
		"--mount", "type=bind,src="+workDir+",dst=/home/runner/_work",
		"--mount", "type=bind,src="+runDir+",dst="+dindSocketDir,
		"--env", "DOCKER_HOST=unix://"+dindSocketPath,
		"--env", "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS=120",
		s.Image,
		"/bin/bash", "-lc", runnerBootstrap,
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
	runnerID, resolveErr := p.resolveRunContainerID(ctx, name, runnerOut)
	if resolveErr != nil {
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", name)
		_ = os.RemoveAll(secretDir)
		return "", resolveErr
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
		"--network", "container:" + runnerID,
		"--mount", "type=bind,src=" + workDir + ",dst=/home/runner/_work",
		"--mount", "type=bind,src=" + runDir + ",dst=" + dindSocketDir,
		"--mount", "type=bind,src=" + externalsDir + ",dst=/home/runner/externals,readonly",
	}
	// The scheduler reserves one resource envelope for the runner and one for
	// the privileged DinD sidecar. Apply the same per-container ceiling here so
	// neither outer container can exceed the resources RunnerHub accounted for.
	if s.Resources.CPUMillis > 0 {
		dindArgs = append(dindArgs, "--cpus", strconv.FormatFloat(float64(s.Resources.CPUMillis)/1000, 'f', 3, 64))
	}
	if s.Resources.MemoryBytes > 0 {
		dindArgs = append(dindArgs, "--memory", strconv.FormatInt(s.Resources.MemoryBytes, 10))
	}
	dindArgs = append(dindArgs,
		p.cfg.DinDImage,
		"dockerd", "--host=unix://"+dindSocketPath, "--group="+runnerDockerGroup,
	)
	if _, err := p.run.Run(ctx, p.cfg.DockerBinary, dindArgs...); err != nil {
		if existing, findErr := p.findExact(ctx, dindName); findErr != nil || existing == "" {
			_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", runnerID)
			_ = os.RemoveAll(secretDir)
			return "", fmt.Errorf("start Docker-in-Docker sidecar: %w", err)
		}
	}

	// Release the long-lived runner process only after the DinD sidecar exists.
	// The runner process itself performs the Docker socket readiness check before
	// starting run.sh. This validates the exact UID/GID/supplementary-group context
	// that will execute workflow steps instead of relying on a separate docker exec.
	if err := os.WriteFile(filepath.Join(runDir, "ready"), []byte("ready\n"), 0o644); err != nil {
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", dindName)
		_, _ = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", runnerID)
		_ = os.RemoveAll(secretDir)
		return "", fmt.Errorf("release Docker-in-Docker runner gate: %w", err)
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

func (p *Provider) resolveRunContainerID(ctx context.Context, name, out string) (string, error) {
	lines := strings.Split(out, "\n")
	nonEmpty := make([]string, 0, len(lines))
	var candidates []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonEmpty = append(nonEmpty, line)
		if detachedContainerIDPattern.MatchString(line) {
			candidates = append(candidates, line)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(nonEmpty) == 1 {
		return nonEmpty[0], nil
	}
	id, err := p.findExact(ctx, name)
	if err != nil {
		return "", fmt.Errorf("resolve Docker container id for %q: %w", name, err)
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("docker created %q but returned no unambiguous container id", name)
	}
	return strings.TrimSpace(id), nil
}

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
				return provider.WorkerState{State: core.WorkerFailed, ExitCode: &code, OOMKilled: dindState.OOMKilled, SafeDetail: "Docker-in-Docker sidecar exited", Diagnostics: p.collectDiagnostics(ctx, sidecar)}, nil
			}
		}
		usage, _ := p.containerUsage(ctx, h.ProviderID, sidecar)
		return provider.WorkerState{State: core.WorkerReady, SafeDetail: "container running", Usage: usage}, nil
	}
	code := state.ExitCode
	detail := "container exited"
	if state.OOMKilled {
		detail += "; OOM killed"
	}
	if msg := sanitize(state.Error); msg != "" {
		detail += "; docker error: " + msg
	}
	if state.Status == "exited" && state.ExitCode == 0 {
		return provider.WorkerState{State: core.WorkerCompleted, ExitCode: &code, OOMKilled: state.OOMKilled, SafeDetail: "container exited successfully"}, nil
	}
	diagnostics := p.collectDiagnostics(ctx, h.ProviderID)
	return provider.WorkerState{State: core.WorkerFailed, ExitCode: &code, OOMKilled: state.OOMKilled, SafeDetail: detail, Diagnostics: diagnostics}, nil
}

type containerState struct {
	Running   bool   `json:"Running"`
	ExitCode  int    `json:"ExitCode"`
	Status    string `json:"Status"`
	OOMKilled bool   `json:"OOMKilled"`
	Error     string `json:"Error"`
}

func (p *Provider) containerUsage(ctx context.Context, runnerID, sidecarID string) (*provider.WorkerUsage, error) {
	ids := []string{strings.TrimSpace(runnerID)}
	if id := strings.TrimSpace(sidecarID); id != "" {
		ids = append(ids, id)
	}
	args := []string{"stats", "--no-stream", "--format", "{{.CPUPerc}}|{{.MemUsage}}"}
	args = append(args, ids...)
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, args...)
	if err != nil {
		return nil, err
	}
	usage := &provider.WorkerUsage{}
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		cpuText := strings.TrimSpace(strings.TrimSuffix(parts[0], "%"))
		cpu, err := strconv.ParseFloat(cpuText, 64)
		if err != nil || cpu < 0 {
			continue
		}
		memText := strings.TrimSpace(strings.SplitN(parts[1], "/", 2)[0])
		mem, err := parseDockerBytes(memText)
		if err != nil {
			continue
		}
		usage.CPUMillis += int64(cpu*10 + 0.5)
		usage.MemoryBytes += mem
		seen++
	}
	if seen == 0 {
		return nil, errors.New("docker stats returned no parseable usage")
	}
	return usage, nil
}

func parseDockerBytes(value string) (int64, error) {
	m := dockerBytePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(m) != 3 {
		return 0, fmt.Errorf("unsupported docker byte value %q", value)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid docker byte value %q", value)
	}
	unit := strings.ToLower(m[2])
	multipliers := map[string]float64{
		"b": 1,
		"kb": 1000,
		"mb": 1000 * 1000,
		"gb": 1000 * 1000 * 1000,
		"tb": 1000 * 1000 * 1000 * 1000,
		"kib": 1 << 10,
		"mib": 1 << 20,
		"gib": 1 << 30,
		"tib": 1 << 40,
	}
	mult, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf("unsupported docker byte unit %q", unit)
	}
	return int64(n*mult + 0.5), nil
}

func (p *Provider) collectDiagnostics(ctx context.Context, id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	out, _ := p.run.Run(ctx, p.cfg.DockerBinary, "logs", "--tail", "80", id)
	return sanitizeDiagnostics(out)
}

func sanitizeDiagnostics(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	lines := strings.Split(s, "\n")
	safeLines := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "running job:") ||
			strings.Contains(lower, "runner.worker") ||
			strings.Contains(lower, "job execution started") {
			safeLines = append(safeLines, "[job output omitted]")
			break
		}
		safeLines = append(safeLines, line)
	}
	s = strings.Join(safeLines, "\n")
	for _, pattern := range diagnosticSecretPatterns {
		s = pattern.re.ReplaceAllString(s, pattern.repl)
	}
	s = strings.TrimSpace(s)
	if len(s) > maxDiagnosticsBytes {
		s = "[truncated]\n" + s[len(s)-maxDiagnosticsBytes:]
	}
	return s
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

	// DinD sidecars use --network container:<runner>, so stop the dependent
	// sidecar before the runner whose network namespace it shares.
	sidecarTarget := sidecarID
	if sidecarTarget == "" && workerID != "" {
		sidecarTarget = workerContainerName(workerID) + "-dind"
	}
	if sidecarTarget != "" {
		_, sidecarErr := p.run.Run(ctx, p.cfg.DockerBinary, "stop", "--time", "10", sidecarTarget)
		if sidecarErr != nil && !strings.Contains(strings.ToLower(sidecarErr.Error()), "no such") {
			return sidecarErr
		}
	}

	_, runnerErr := p.run.Run(ctx, p.cfg.DockerBinary, "stop", "--time", "10", h.ProviderID)
	if runnerErr != nil && strings.Contains(strings.ToLower(runnerErr.Error()), "no such") {
		runnerErr = nil
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
	}

	// A DinD sidecar joins the runner's network namespace with
	// --network container:<runner>. Docker can reject removal of the runner
	// while that dependent sidecar still exists, so always remove it first.
	sidecarTarget := sidecarID
	if sidecarTarget == "" && workerID != "" {
		sidecarTarget = workerContainerName(workerID) + "-dind"
	}
	if sidecarTarget != "" {
		_, err := p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", sidecarTarget)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such") {
			return err
		}
	}

	if h.ProviderID != "" {
		_, err := p.run.Run(ctx, p.cfg.DockerBinary, "rm", "-f", h.ProviderID)
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
