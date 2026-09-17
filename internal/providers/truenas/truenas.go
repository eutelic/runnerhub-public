package truenas

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
	runnerUID = 1001
	runnerGID = 1001
)

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

type Config struct {
	Midclt        string
	HostID        string
	Root          string
	AllowedImages []string
}

type Provider struct {
	cfg     Config
	run     Runner
	allowed map[string]struct{}
}

func New(config Config) (*Provider, error) {
	return newWithRunner(config, execRunner{})
}

func newWithRunner(config Config, runner Runner) (*Provider, error) {
	if config.Midclt == "" {
		config.Midclt = "midclt"
	}
	if strings.TrimSpace(config.Root) == "" {
		return nil, errors.New("truenas root is required; use a dedicated dataset path under /mnt/<pool>")
	}
	if !filepath.IsAbs(config.Root) {
		return nil, errors.New("truenas root must be absolute")
	}
	if len(config.AllowedImages) == 0 {
		config.AllowedImages = []string{images.DefaultRunnerImage}
	}

	allowed := map[string]struct{}{}
	for _, image := range config.AllowedImages {
		allowed[image] = struct{}{}
	}
	return &Provider{cfg: config, run: runner, allowed: allowed}, nil
}

func (p *Provider) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	if _, err := p.run.Run(ctx, p.cfg.Midclt, "call", "system.version"); err != nil {
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{
		Provider:       "truenas",
		OS:             "linux",
		Architecture:   runtime.GOARCH,
		Capacity:       hostinfo.Capacity(p.cfg.Root),
		IsolationModes: []core.IsolationMode{core.IsolationContainerTrusted},
		Features: map[string]bool{
			"customApps":        true,
			"structuredCompose": true,
		},
		Labels: map[string]string{
			"appliance":                   "truenas",
			"isolation.container-trusted": "true",
		},
	}, nil
}

func (p *Provider) Validate(_ context.Context, spec provider.WorkerSpec) error {
	if strings.TrimSpace(spec.WorkerID) == "" || strings.TrimSpace(spec.PoolID) == "" || strings.TrimSpace(spec.RunnerName) == "" {
		return errors.New("worker id, pool id and runner name are required")
	}
	if spec.Isolation != core.IsolationContainerTrusted {
		return errors.New("truenas custom-app provider currently supports container-trusted only")
	}
	if _, ok := p.allowed[spec.Image]; !ok {
		return fmt.Errorf("image %q is not allowlisted", spec.Image)
	}
	if spec.JITConfig == "" {
		return errors.New("jit config required")
	}
	return nil
}

func writeJITSecret(path string, data []byte) error {
	// TrueNAS datasets can inherit ACLs that narrow requested create modes.
	// Explicitly hand the JIT secret to the non-root runner identity and then
	// make it owner-read-only before creating the custom app.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	if err := os.Chown(path, runnerUID, runnerGID); err != nil {
		if os.Geteuid() != 0 {
			if chmodErr := os.Chmod(path, 0o444); chmodErr != nil {
				return fmt.Errorf("set JIT secret fallback mode after chown failure: %w", chmodErr)
			}
			return nil
		}
		return fmt.Errorf("set JIT secret owner to %d:%d: %w", runnerUID, runnerGID, err)
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

func (p *Provider) Create(ctx context.Context, spec provider.WorkerSpec) (provider.WorkerHandle, error) {
	if spec.Image == "" {
		spec.Image = images.DefaultRunnerImage
	}
	if err := p.Validate(ctx, spec); err != nil {
		return provider.WorkerHandle{}, err
	}

	app := appName(spec.WorkerID)
	root := filepath.Join(p.cfg.Root, "workers", app)
	if !strings.HasPrefix(filepath.Clean(root), filepath.Clean(p.cfg.Root)+string(filepath.Separator)) {
		return provider.WorkerHandle{}, errors.New("generated path escaped truenas root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return provider.WorkerHandle{}, err
	}
	jitPath := filepath.Join(root, "jitconfig")
	if err := writeJITSecret(jitPath, []byte(spec.JITConfig)); err != nil {
		_ = os.RemoveAll(root)
		return provider.WorkerHandle{}, err
	}

	runnerService := map[string]any{
		"image":   spec.Image,
		"restart": "no",
		"command": []string{"/bin/bash", "-lc", `exec /home/runner/run.sh --jitconfig "$(cat /run/runnerhub/jitconfig)"`},
		"volumes": []string{jitPath + ":/run/runnerhub/jitconfig:ro"},
		"tmpfs": []string{
			"/home/runner/_work:rw,exec,nosuid,nodev,size=" + strconv.FormatInt(workSize(spec), 10),
		},
		"labels": map[string]string{
			"io.runnerhub.worker": spec.WorkerID,
			"io.runnerhub.pool":   spec.PoolID,
		},
		"security_opt": []string{"no-new-privileges:true"},
	}
	if spec.Resources.CPUMillis > 0 {
		runnerService["cpus"] = strconv.FormatFloat(float64(spec.Resources.CPUMillis)/1000, 'f', 3, 64)
	}
	if spec.Resources.MemoryBytes > 0 {
		runnerService["mem_limit"] = strconv.FormatInt(spec.Resources.MemoryBytes, 10)
	}
	compose := map[string]any{
		"services": map[string]any{
			"runner": runnerService,
		},
	}
	payload := map[string]any{
		"app_name":              app,
		"custom_app":            true,
		"custom_compose_config": compose,
	}
	body, _ := json.Marshal(payload)

	if _, err := p.run.Run(ctx, p.cfg.Midclt, "call", "-job", "app.create", string(body)); err != nil {
		if _, inspectErr := p.Inspect(ctx, provider.WorkerHandle{ProviderID: app, HostID: p.cfg.HostID}); inspectErr == nil {
			return provider.WorkerHandle{ProviderID: app, HostID: p.cfg.HostID}, nil
		}
		_ = os.RemoveAll(root)
		return provider.WorkerHandle{}, err
	}
	return provider.WorkerHandle{ProviderID: app, HostID: p.cfg.HostID}, nil
}

func (p *Provider) Inspect(ctx context.Context, handle provider.WorkerHandle) (provider.WorkerState, error) {
	if strings.TrimSpace(handle.ProviderID) == "" {
		return provider.WorkerState{}, errors.New("app id required")
	}
	output, err := p.run.Run(ctx, p.cfg.Midclt, "call", "app.get_instance", handle.ProviderID, "{}")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return provider.WorkerState{State: core.WorkerDeleted}, nil
		}
		return provider.WorkerState{}, err
	}

	var value struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return provider.WorkerState{}, err
	}
	switch value.State {
	case "RUNNING", "DEPLOYING":
		return provider.WorkerState{State: core.WorkerReady, SafeDetail: "truenas app " + strings.ToLower(value.State)}, nil
	case "STOPPED":
		return provider.WorkerState{State: core.WorkerCompleted, SafeDetail: "truenas app stopped"}, nil
	case "CRASHED":
		return provider.WorkerState{State: core.WorkerFailed, SafeDetail: "truenas app crashed"}, nil
	default:
		return provider.WorkerState{State: core.WorkerStarting, SafeDetail: "truenas app " + value.State}, nil
	}
}

func (p *Provider) Stop(ctx context.Context, handle provider.WorkerHandle) error {
	_, err := p.run.Run(ctx, p.cfg.Midclt, "call", "-job", "app.stop", handle.ProviderID)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

func (p *Provider) Delete(ctx context.Context, handle provider.WorkerHandle) error {
	if strings.TrimSpace(handle.ProviderID) == "" {
		return nil
	}
	_, err := p.run.Run(ctx, p.cfg.Midclt, "call", "-job", "app.delete", handle.ProviderID)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		return err
	}
	_ = os.RemoveAll(filepath.Join(p.cfg.Root, "workers", handle.ProviderID))
	return nil
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

func appName(id string) string {
	name := strings.ToLower(id)
	name = nonName.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "worker"
	}
	name = "rh-" + name
	if len(name) > 40 {
		name = name[:40]
	}
	return strings.Trim(name, "-")
}

func workSize(spec provider.WorkerSpec) int64 {
	if spec.Resources.DiskBytes > 0 {
		return spec.Resources.DiskBytes
	}
	return 20 << 30
}
