package agentupdatedocker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agentupdate"
)

type launchCLI struct {
	*fakeCLI
	lastRun    []string
	runs       [][]string
	startCount int
}

func (c *launchCLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "run" {
		c.lastRun = append([]string{}, args...)
		c.runs = append(c.runs, append([]string{}, args...))
		c.startCount++
		name := ""
		labels := map[string]string{}
		env := []string{}
		binds := []string{}
		restart := ""
		for i := 1; i < len(args)-1; i++ {
			switch args[i] {
			case "--name":
				name = args[i+1]
			case "--label":
				key, val, ok := strings.Cut(args[i+1], "=")
				if ok {
					labels[key] = val
				}
			case "--env":
				env = append(env, args[i+1])
			case "--volume":
				binds = append(binds, args[i+1])
			case "--restart":
				restart = args[i+1]
			}
		}
		if name == "" {
			return nil, errors.New("missing helper name")
		}
		id := strings.Repeat("e", 64)
		if c.startCount > 1 {
			id = strings.Repeat("f", 64)
		}
		d := fixture()
		d.ID = id
		d.Name = "/" + name
		d.State.Running = true
		d.State.StartedAt = time.Now().Add(-11 * time.Second)
		d.Config.Image = args[len(args)-1]
		d.Config.Labels = labels
		d.Config.Env = env
		d.HostConfig.Binds = binds
		d.HostConfig.RestartPolicy.Name = restart
		c.fakeCLI.containers[id] = &d
		return []byte(id), nil
	}
	return c.fakeCLI.Run(ctx, args...)
}
func updateCommand() agent.AgentUpdateSpec {
	return agent.AgentUpdateSpec{
		HostID: "host-compute", OperationID: "op-123",
		TargetImage:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("a", 64),
		TargetRevision: strings.Repeat("b", 40),
		HelperImage:    updaterRepository + "@sha256:" + strings.Repeat("c", 64),
	}
}
func TestLauncherStartsDetachedPinnedUpdaterWithoutBearerCredential(t *testing.T) {
	cli := &launchCLI{fakeCLI: newFakeCLI()}
	spec := updateCommand()
	launcher := Launcher{Docker: cli, ApprovedHelperImage: spec.HelperImage, ControlURL: "https://control.example"}
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if cli.startCount != 2 {
		t.Fatalf("expected watchdog + updater, started=%d", cli.startCount)
	}
	watchdogArgs := strings.Join(cli.runs[0], " ")
	if !strings.Contains(watchdogArgs, "--restart unless-stopped") ||
		!strings.Contains(watchdogArgs, "RUNNERHUB_UPDATE_WATCHDOG=true") ||
		!strings.Contains(watchdogArgs, "RUNNERHUB_UPDATE_RECOVERY_OWNER=") ||
		!strings.Contains(watchdogArgs, "io.runnerhub.update.role=watchdog") {
		t.Fatalf("watchdog is not an independent restartable exact-host helper: %s", watchdogArgs)
	}
	args := strings.Join(cli.lastRun, " ")
	if !strings.Contains(args, "--restart no") || strings.Contains(args, "RUNNERHUB_UPDATE_WATCHDOG=true") {
		t.Fatalf("normal updater unexpectedly restarts or runs as watchdog: %s", args)
	}
	for _, want := range []string{
		"--volume /var/run/docker.sock:/var/run/docker.sock",
		"--volume /var/lib/runnerhub:/var/lib/runnerhub:rw",
		"io.runnerhub.update.operation=op-123",
		"RUNNERHUB_UPDATE_HOST_ID=host-compute",
		"io.runnerhub.update.role=updater",
		spec.TargetImage, spec.HelperImage,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing launch option: %s", want)
		}
	}
	if strings.Contains(args, "not-logged") || strings.Contains(args, "host-secret") || strings.Contains(args, "--privileged") ||
		strings.Contains(watchdogArgs, "not-logged") || strings.Contains(watchdogArgs, "host-secret") {
		t.Fatal("launcher exposed credential or unrestricted privilege flag")
	}
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatalf("idempotent launch: %v", err)
	}
	if cli.startCount != 2 {
		t.Fatalf("duplicate helper launch started %d containers", cli.startCount)
	}
}
func TestLauncherRefusesCustomHelperAndInvalidOperation(t *testing.T) {
	cli := &launchCLI{fakeCLI: newFakeCLI()}
	spec := updateCommand()
	launcher := Launcher{Docker: cli, ApprovedHelperImage: spec.HelperImage, ControlURL: "https://control.example"}
	wrong := spec
	wrong.HelperImage = "ghcr.io/other/updater@sha256:" + strings.Repeat("c", 64)
	if err := launcher.Launch(context.Background(), wrong); err == nil {
		t.Fatal("unapproved helper accepted")
	}
	wrong = spec
	wrong.OperationID = "../../not-safe"
	if err := launcher.Launch(context.Background(), wrong); err == nil {
		t.Fatal("unsafe operation ID accepted")
	}
	wrong = spec
	wrong.TargetImage = agentupdate.AgentImage + ":latest"
	if err := launcher.Launch(context.Background(), wrong); err == nil {
		t.Fatal("moving agent target accepted")
	}
	if cli.startCount != 0 {
		t.Fatalf("unapproved helper launched %d times", cli.startCount)
	}
}

func TestWatchdogRestartPolicyMustBeEstablishedBeforeAgentReplacement(t *testing.T) {
	cli := &launchCLI{fakeCLI: newFakeCLI()}
	spec := updateCommand()
	launcher := Launcher{Docker: cli, ApprovedHelperImage: spec.HelperImage, ControlURL: "https://control.example"}
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	watchdog := cli.containers[strings.Repeat("e", 64)]
	watchdog.State.StartedAt = time.Now()
	if err := launcher.checkWatchdog(context.Background(), spec); !errors.Is(err, errWatchdogWarming) {
		t.Fatalf("new watchdog was incorrectly considered restart-ready: %v", err)
	}
	watchdog.State.StartedAt = time.Now().Add(-12 * time.Second)
	watchdog.HostConfig.RestartPolicy.Name = "no"
	if err := launcher.checkWatchdog(context.Background(), spec); err == nil {
		t.Fatal("watchdog without restart policy was approved")
	}
	watchdog.HostConfig.RestartPolicy.Name = "unless-stopped"
	if err := launcher.checkWatchdog(context.Background(), spec); err != nil {
		t.Fatalf("mature watchdog with persistent restart policy was rejected: %v", err)
	}
}

// A 10-second-running container with the right image and role labels is not
// sufficient: its immutable recovery identity, Docker socket and persistent
// credential/journal mount must also match before the old agent is stopped.
func TestWatchdogRejectsUnusableRecoveryConfiguration(t *testing.T) {
	cli := &launchCLI{fakeCLI: newFakeCLI()}
	spec := updateCommand()
	launcher := Launcher{Docker: cli, ApprovedHelperImage: spec.HelperImage, ControlURL: "https://control.example"}
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	watchdogID := strings.Repeat("e", 64)
	original := *cli.containers[watchdogID]
	if err := launcher.checkWatchdog(context.Background(), spec); err != nil {
		t.Fatalf("valid watchdog rejected: %v", err)
	}
	tests := []struct {
		name   string
		change func(*dockerInspect)
	}{
		{"missing-docker-socket", func(d *dockerInspect) { d.HostConfig.Binds = []string{"/var/lib/runnerhub:/var/lib/runnerhub:rw"} }},
		{"read-only-journal", func(d *dockerInspect) {
			d.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock", "/var/lib/runnerhub:/var/lib/runnerhub:ro"}
		}},
		{"unexpected-mount", func(d *dockerInspect) { d.HostConfig.Binds = append(d.HostConfig.Binds, "/tmp:/tmp") }},
		{"missing-recovery-owner", func(d *dockerInspect) {
			d.Config.Env = filterUpdateEnv(d.Config.Env, "RUNNERHUB_UPDATE_RECOVERY_OWNER")
		}},
		{"mismatched-operation", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_UPDATE_OPERATION_ID", "op-different")
		}},
		{"wrong-target-revision", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_UPDATE_TARGET_REVISION", strings.Repeat("c", 40))
		}},
		{"wrong-control-url", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_CONTROL_PLANE_URL", "https://wrong.example")
		}},
		{"wrong-watchdog-mode", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_UPDATE_WATCHDOG", "false")
		}},
		{"duplicate-host-id", func(d *dockerInspect) { d.Config.Env = append(d.Config.Env, "RUNNERHUB_UPDATE_HOST_ID="+spec.HostID) }},
		{"unrequested-insecure-http", func(d *dockerInspect) { d.Config.Env = append(d.Config.Env, "RUNNERHUB_ALLOW_INSECURE_HTTP=true") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := original
			d.Config.Env = append([]string(nil), original.Config.Env...)
			d.HostConfig.Binds = append([]string(nil), original.HostConfig.Binds...)
			tt.change(&d)
			cli.containers[watchdogID] = &d
			if err := launcher.checkWatchdog(context.Background(), spec); err == nil {
				t.Fatal("watchdog without safe recovery configuration was accepted")
			}
		})
	}
	cli.containers[watchdogID] = &original
	if err := launcher.checkWatchdog(context.Background(), spec); err != nil {
		t.Fatalf("valid watchdog should recover after fixture reset: %v", err)
	}
}

func filterUpdateEnv(entries []string, key string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return out
}
func replaceUpdateEnv(entries []string, key, value string) []string {
	out := append([]string(nil), entries...)
	for i, entry := range out {
		if strings.HasPrefix(entry, key+"=") {
			out[i] = key + "=" + value
		}
	}
	return out
}

// Idempotent retries may only acknowledge a matching updater; checking the
// image, host and operation labels alone is insufficient to rule out a
// stranded or misconfigured helper with the same Docker name.
func TestLauncherRefusesMismatchedExistingUpdater(t *testing.T) {
	cli := &launchCLI{fakeCLI: newFakeCLI()}
	spec := updateCommand()
	launcher := Launcher{Docker: cli, ApprovedHelperImage: spec.HelperImage, ControlURL: "https://control.example"}
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	updaterID := strings.Repeat("f", 64)
	original := *cli.containers[updaterID]
	tests := []struct {
		name   string
		change func(*dockerInspect)
	}{
		{"missing-updater-role", func(d *dockerInspect) { delete(d.Config.Labels, "io.runnerhub.update.role") }},
		{"wrong-target", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_UPDATE_TARGET_IMAGE", agentupdate.AgentImage+"@sha256:"+strings.Repeat("d", 64))
		}},
		{"wrong-control-url", func(d *dockerInspect) {
			d.Config.Env = replaceUpdateEnv(d.Config.Env, "RUNNERHUB_CONTROL_PLANE_URL", "https://unexpected.example")
		}},
		{"missing-state-volume", func(d *dockerInspect) { d.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"} }},
		{"wrong-restart-policy", func(d *dockerInspect) { d.HostConfig.RestartPolicy.Name = "unless-stopped" }},
		{"unexpected-watchdog-mode", func(d *dockerInspect) { d.Config.Env = append(d.Config.Env, "RUNNERHUB_UPDATE_WATCHDOG=true") }},
		{"unexpected-recovery-owner", func(d *dockerInspect) {
			d.Config.Env = append(d.Config.Env, "RUNNERHUB_UPDATE_RECOVERY_OWNER="+strings.Repeat("a", 32))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := original
			d.Config.Env = append([]string(nil), original.Config.Env...)
			d.HostConfig.Binds = append([]string(nil), original.HostConfig.Binds...)
			d.Config.Labels = map[string]string{}
			for key, value := range original.Config.Labels {
				d.Config.Labels[key] = value
			}
			tt.change(&d)
			cli.containers[updaterID] = &d
			if err := launcher.Launch(context.Background(), spec); err == nil {
				t.Fatal("mismatched running updater was accepted as an idempotent command")
			}
			if cli.startCount != 2 {
				t.Fatal("retry launched duplicate helpers")
			}
		})
	}
	cli.containers[updaterID] = &original
	if err := launcher.Launch(context.Background(), spec); err != nil {
		t.Fatalf("correct existing updater rejected: %v", err)
	}
}
