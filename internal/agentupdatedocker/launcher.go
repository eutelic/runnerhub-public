package agentupdatedocker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agentupdate"
)

const updaterRepository = "ghcr.io/eutelic/runnerhub-agent-updater"

var updaterImagePattern = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent-updater@sha256:[a-f0-9]{64}$`)
var safeOperationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var recoveryOwnerPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Launcher launches one Docker helper container independently of the agent.
// It never passes per-host bearer tokens, SSH credentials, or Docker login
// secrets in command arguments. The helper reads the existing bind-mounted
// 0600 agent credential file after it starts.
type Launcher struct {
	Docker              CLI
	ApprovedHelperImage string
	ControlURL          string
	AllowInsecureHTTP   bool
}

type helperInspect struct {
	ID    string `json:"Id"`
	State struct {
		Running   bool      `json:"Running"`
		StartedAt time.Time `json:"StartedAt"`
	} `json:"State"`
	HostConfig struct {
		Binds         []string `json:"Binds"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
		Env    []string          `json:"Env"`
	} `json:"Config"`
}

func (l *Launcher) client() CLI {
	if l.Docker != nil {
		return l.Docker
	}
	return ExecCLI{}
}

var errWatchdogWarming = errors.New("Docker watchdog restart policy is not established yet")

// The watchdog must have working access to Docker and the durable state mount,
// not merely look healthy from a restart policy and labels. Otherwise the
// updater could stop the agent with no usable independent recovery path.
func watchdogBindsMatch(binds []string) bool {
	if len(binds) != 2 {
		return false
	}
	socket, state := false, false
	for _, bind := range binds {
		switch bind {
		case "/var/run/docker.sock:/var/run/docker.sock":
			if socket {
				return false
			}
			socket = true
		case "/var/lib/runnerhub:/var/lib/runnerhub:rw":
			if state {
				return false
			}
			state = true
		default:
			return false
		}
	}
	return socket && state
}

func (l *Launcher) helperEnvMatches(env []string, spec agent.AgentUpdateSpec, watchdog bool) bool {
	required := map[string]string{
		"RUNNERHUB_UPDATE_HOST_ID":         spec.HostID,
		"RUNNERHUB_UPDATE_OPERATION_ID":    spec.OperationID,
		"RUNNERHUB_UPDATE_TARGET_IMAGE":    spec.TargetImage,
		"RUNNERHUB_UPDATE_TARGET_REVISION": spec.TargetRevision,
		"RUNNERHUB_CONTROL_PLANE_URL":      l.ControlURL,
	}
	if watchdog {
		required["RUNNERHUB_UPDATE_WATCHDOG"] = "true"
	}
	if l.AllowInsecureHTTP {
		required["RUNNERHUB_ALLOW_INSECURE_HTTP"] = "true"
	}
	seen := map[string]bool{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return false
		}
		if key == "RUNNERHUB_UPDATE_RECOVERY_OWNER" {
			if !watchdog || seen[key] || !recoveryOwnerPattern.MatchString(value) {
				return false
			}
			seen[key] = true
			continue
		}
		expected, needed := required[key]
		if needed {
			if seen[key] || value != expected {
				return false
			}
			seen[key] = true
			continue
		}
		// Reject unexpected updater settings that might change recovery behavior.
		if strings.HasPrefix(key, "RUNNERHUB_UPDATE_") || key == "RUNNERHUB_ALLOW_INSECURE_HTTP" {
			return false
		}
	}
	if watchdog && !seen["RUNNERHUB_UPDATE_RECOVERY_OWNER"] {
		return false
	}
	for key := range required {
		if !seen[key] {
			return false
		}
	}
	return true
}

// Available is called during Docker-agent startup before advertising
// feature.agentSelfUpdate. Never advertise a helper that is missing or
// configured with a mutable/untrusted image reference.
func (l *Launcher) Available(ctx context.Context) error {
	if !updaterImagePattern.MatchString(l.ApprovedHelperImage) {
		return errors.New("pinned official updater image is not configured")
	}
	if l.ControlURL == "" {
		return errors.New("updater control URL is missing")
	}
	// Do not block agent startup/enrollment indefinitely on a slow registry.
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cli := l.client()
	out, err := cli.Run(readyCtx, "image", "inspect", "--format", "{{.Id}}", l.ApprovedHelperImage)
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return nil
	}
	if _, err := cli.Run(readyCtx, "pull", l.ApprovedHelperImage); err != nil {
		return errors.New("pinned updater image could not be pulled")
	}
	out, err = cli.Run(readyCtx, "image", "inspect", "--format", "{{.Id}}", l.ApprovedHelperImage)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return errors.New("pinned updater image not verified locally")
	}
	return nil
}

// A separate restartable watchdog must be active before the original helper
// can stop the enrolled agent. It uses the same pinned image and exact host
// operation, but runs independently of the main update command.
func (l *Launcher) checkWatchdog(ctx context.Context, spec agent.AgentUpdateSpec) error {
	name := "runnerhub-agent-watchdog-" + spec.OperationID
	raw, err := l.client().Run(ctx, "inspect", name)
	if err != nil {
		return errors.New("independent updater watchdog is missing")
	}
	var records []helperInspect
	if json.Unmarshal(raw, &records) != nil || len(records) != 1 {
		return errors.New("ambiguous updater watchdog identity")
	}
	w := records[0]
	if !exactContainerID.MatchString(w.ID) || !w.State.Running ||
		w.Config.Image != spec.HelperImage ||
		w.Config.Labels["io.runnerhub.update.host"] != spec.HostID ||
		w.Config.Labels["io.runnerhub.update.operation"] != spec.OperationID ||
		w.Config.Labels["io.runnerhub.update.role"] != "watchdog" ||
		w.HostConfig.RestartPolicy.Name != "unless-stopped" ||
		!watchdogBindsMatch(w.HostConfig.Binds) ||
		!l.helperEnvMatches(w.Config.Env, spec, true) {
		return errors.New("updater watchdog is not the exact restartable running helper")
	}
	// Docker only establishes the restart policy after the container has been
	// running for at least 10 seconds. Require that before replacing the old
	// agent, otherwise a daemon crash during the initial window may strand it.
	if w.State.StartedAt.IsZero() || time.Since(w.State.StartedAt) < 10*time.Second {
		return errWatchdogWarming
	}
	return nil
}

func (l *Launcher) waitWatchdogReady(ctx context.Context, spec agent.AgentUpdateSpec) error {
	readyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := l.checkWatchdog(readyCtx, spec)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errWatchdogWarming) {
			return err
		}
		select {
		case <-readyCtx.Done():
			return errors.New("watchdog did not establish a restart policy before timeout")
		case <-ticker.C:
		}
	}
}
func (l *Launcher) ensureWatchdog(ctx context.Context, spec agent.AgentUpdateSpec, base []string) error {
	if err := l.checkWatchdog(ctx, spec); err == nil {
		return nil
	} else if errors.Is(err, errWatchdogWarming) {
		return l.waitWatchdogReady(ctx, spec)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("watchdog ownership ID generation failed")
	}
	owner := hex.EncodeToString(random[:])
	name := "runnerhub-agent-watchdog-" + spec.OperationID
	args := append([]string{"run", "-d", "--name", name, "--restart", "unless-stopped"}, base...)
	args = append(args,
		"--label", "io.runnerhub.update.role=watchdog",
		"--env", "RUNNERHUB_UPDATE_WATCHDOG=true",
		"--env", "RUNNERHUB_UPDATE_RECOVERY_OWNER="+owner, spec.HelperImage)
	if _, err := l.client().Run(ctx, args...); err != nil {
		return errors.New("detached watchdog could not be started")
	}
	return l.waitWatchdogReady(ctx, spec)
}

func (l *Launcher) Launch(ctx context.Context, spec agent.AgentUpdateSpec) error {
	if spec.HostID == "" || !safeOperationID.MatchString(spec.OperationID) {
		return errors.New("exact host ID and safe operation ID required")
	}
	target := agentupdate.Target{Image: spec.TargetImage, Revision: spec.TargetRevision}
	if err := target.Validate(); err != nil {
		return err
	}
	if !updaterImagePattern.MatchString(spec.HelperImage) ||
		spec.HelperImage != l.ApprovedHelperImage {
		return errors.New("helper image is not the configured pinned official updater")
	}
	if l.ControlURL == "" {
		return errors.New("control-plane URL not configured for updater")
	}
	// The agent's command loop must not hang forever when Docker or the
	// registry stalls. A timed-out launch is reported as failed, without
	// permission to replace the original container.
	launchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	name := "runnerhub-agent-updater-" + spec.OperationID
	existing, err := l.client().Run(launchCtx, "inspect", name)
	if err == nil {
		var containers []helperInspect
		if json.Unmarshal(existing, &containers) != nil || len(containers) != 1 ||
			containers[0].Config.Image != spec.HelperImage ||
			!containers[0].State.Running ||
			containers[0].Config.Labels["io.runnerhub.update.host"] != spec.HostID ||
			containers[0].Config.Labels["io.runnerhub.update.operation"] != spec.OperationID ||
			containers[0].Config.Labels["io.runnerhub.update.role"] != "updater" ||
			containers[0].HostConfig.RestartPolicy.Name != "no" ||
			!watchdogBindsMatch(containers[0].HostConfig.Binds) ||
			!l.helperEnvMatches(containers[0].Config.Env, spec, false) {
			return errors.New("update helper name collision with an unrecognized container")
		}
		// Preserve idempotency, and verify that the independent restartable
		// watchdog has not disappeared while the normal helper is running.
		return l.checkWatchdog(launchCtx, spec)
	}
	if _, err := l.client().Run(launchCtx, "pull", spec.HelperImage); err != nil {
		return fmt.Errorf("pinned updater image pull failed: %w", err)
	}
	base := []string{
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"--volume", "/var/lib/runnerhub:/var/lib/runnerhub:rw",
		"--label", "io.runnerhub.update.host=" + spec.HostID,
		"--label", "io.runnerhub.update.operation=" + spec.OperationID,
		"--env", "RUNNERHUB_UPDATE_HOST_ID=" + spec.HostID,
		"--env", "RUNNERHUB_UPDATE_OPERATION_ID=" + spec.OperationID,
		"--env", "RUNNERHUB_UPDATE_TARGET_IMAGE=" + spec.TargetImage,
		"--env", "RUNNERHUB_UPDATE_TARGET_REVISION=" + spec.TargetRevision,
		"--env", "RUNNERHUB_CONTROL_PLANE_URL=" + l.ControlURL,
	}
	if l.AllowInsecureHTTP {
		base = append(base, "--env", "RUNNERHUB_ALLOW_INSECURE_HTTP=true")
	}
	// Inherit the host agent's explicit DNS servers; default Docker DNS
	// might be unable to resolve private tailnet control-plane names.
	current, err := l.client().Run(launchCtx, "inspect", agentName)
	if err != nil {
		return errors.New("installed agent topology cannot be verified")
	}
	var currentAgent []dockerInspect
	if json.Unmarshal(current, &currentAgent) != nil || len(currentAgent) != 1 ||
		verifyTopology(currentAgent[0]) != nil {
		return errors.New("existing Docker agent topology is unrecognized")
	}
	for _, server := range currentAgent[0].HostConfig.DNS {
		if strings.ContainsAny(server, " \r\n") || server == "" {
			return errors.New("invalid agent DNS configuration")
		}
		base = append(base, "--dns", server)
	}
	// The watchdog survives the old agent and both helper/daemon restarts.
	// It must be verified before the normal updater is permitted to launch.
	if err := l.ensureWatchdog(launchCtx, spec, base); err != nil {
		return err
	}
	// The normal update helper itself is never restarted automatically: after
	// an interrupted swap the *watchdog* uses the durable recovery journal.
	args := append([]string{"run", "-d", "--name", name, "--restart", "no"}, base...)
	args = append(args, "--label", "io.runnerhub.update.role=updater", spec.HelperImage)
	if _, err := l.client().Run(launchCtx, args...); err != nil {
		return errors.New("detached updater failed to launch")
	}
	return nil
}
