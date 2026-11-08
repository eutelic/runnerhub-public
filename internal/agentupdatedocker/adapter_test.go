package agentupdatedocker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/eutelic/runnerhub/internal/agentupdate"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var oldID = strings.Repeat("a", 64)
var newID = strings.Repeat("b", 64)

func fixture() dockerInspect {
	var d dockerInspect
	d.ID = oldID
	d.Name = "/runnerhub-agent"
	d.ImageID = "sha256:old"
	d.State.Running = true
	d.Config.Image = agentupdate.AgentImage + ":sha-previous"
	d.Config.Env = []string{"PATH=/usr/local/bin", "RUNNERHUB_CONTROL_PLANE_URL=https://control.example", "RUNNERHUB_HOST_NAME=compute", "RUNNERHUB_PROVIDER=docker"}
	d.Config.Entrypoint = []string{entrypoint}
	d.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock", "/var/lib/runnerhub:/var/lib/runnerhub"}
	d.HostConfig.RestartPolicy.Name = "unless-stopped"
	d.HostConfig.NetworkMode = "bridge"
	return d
}
func TestTopologyAcceptsOnlyKnownDockerAgentConfiguration(t *testing.T) {
	base := fixture()
	if err := verifyTopology(base); err != nil {
		t.Fatalf("known bootstrap agent rejected: %v", err)
	}
	tests := []struct {
		name   string
		change func(*dockerInspect)
	}{
		{"not-running", func(d *dockerInspect) { d.State.Running = false }},
		{"unexpected-name", func(d *dockerInspect) { d.Name = "/wrong-agent" }},
		{"wrong-image", func(d *dockerInspect) { d.Config.Image = "ubuntu:latest" }},
		{"custom-entrypoint", func(d *dockerInspect) { d.Config.Entrypoint = []string{"/bin/sh"} }},
		{"missing-identity", func(d *dockerInspect) { d.Config.Env = []string{"RUNNERHUB_PROVIDER=docker"} }},
		{"persistent-token", func(d *dockerInspect) { d.Config.Env = append(d.Config.Env, "RUNNERHUB_ENROLLMENT_TOKEN=secret") }},
		{"privileged", func(d *dockerInspect) { d.HostConfig.Privileged = true }},
		{"unexpected-mount", func(d *dockerInspect) { d.HostConfig.Binds = append(d.HostConfig.Binds, "/etc/shadow:/etc/shadow") }},
		{"missing-state", func(d *dockerInspect) { d.HostConfig.Binds = d.HostConfig.Binds[:1] }},
		{"extra-capability", func(d *dockerInspect) { d.HostConfig.CapAdd = []string{"SYS_ADMIN"} }},
		{"host-network", func(d *dockerInspect) { d.HostConfig.NetworkMode = "host" }},
		{"published-port", func(d *dockerInspect) { d.HostConfig.PortBindings = map[string]any{"80/tcp": true} }},
		{"untrusted-label", func(d *dockerInspect) { d.Config.Labels = map[string]string{"secret": "must-not-leak"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fixture()
			tt.change(&d)
			if err := verifyTopology(d); err == nil {
				t.Fatal("unsafe agent topology accepted")
			}
		})
	}
	d := fixture()
	d.HostConfig.Binds = append(d.HostConfig.Binds, "/sys/fs/cgroup:/sys/fs/cgroup-host:rw")
	d.HostConfig.CgroupnsMode = "host"
	d.HostConfig.CapAdd = []string{"SYS_ADMIN"}
	if err := verifyTopology(d); err != nil {
		t.Fatalf("supported publisher cgroup configuration rejected: %v", err)
	}
}

type fakeCLI struct {
	mu            sync.Mutex
	containers    map[string]*dockerInspect
	calls         []string
	fail          string
	imageRevision string
}

func newFakeCLI() *fakeCLI {
	old := fixture()
	return &fakeCLI{containers: map[string]*dockerInspect{old.ID: &old}}
}
func (f *fakeCLI) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 0 {
		return nil, errors.New("empty Docker call")
	}
	cmd := args[0]
	f.calls = append(f.calls, strings.Join(args[:min(2, len(args))], " "))
	if f.fail == cmd {
		return nil, errors.New("simulated Docker error")
	}
	find := func(s string) (*dockerInspect, error) {
		if d := f.containers[s]; d != nil {
			return d, nil
		}
		for _, d := range f.containers {
			if d.Name == "/"+s {
				return d, nil
			}
		}
		return nil, errors.New("container not found")
	}
	switch cmd {
	case "inspect":
		d, e := find(args[1])
		if e != nil {
			return nil, e
		}
		return json.Marshal([]dockerInspect{*d})
	case "ps":
		if len(args) != 7 || args[1] != "-a" || args[2] != "--no-trunc" ||
			args[3] != "--filter" || !strings.HasPrefix(args[4], "id=") ||
			args[5] != "--format" || args[6] != "{{.ID}}" {
			return nil, errors.New("unsupported Docker inventory query")
		}
		id := strings.TrimPrefix(args[4], "id=")
		if d := f.containers[id]; d != nil {
			return []byte(d.ID + "\\n"), nil
		}
		return []byte{}, nil
	case "pull":
		return []byte("pulled"), nil
	case "image":
		if len(args) >= 4 && args[3] == "{{json .Config.Labels}}" {
			rev := f.imageRevision
			if rev == "" {
				rev = strings.Repeat("d", 40)
			}
			return json.Marshal(map[string]string{
				"org.opencontainers.image.revision": rev,
				"org.opencontainers.image.source":   "https://github.com/eutelic/runnerhub",
			})
		}
		return []byte("sha256:target\n"), nil
	case "create":
		name := ""
		for i := 1; i < len(args)-1; i++ {
			if args[i] == "--name" {
				name = args[i+1]
				break
			}
		}
		if name == "" {
			return nil, errors.New("missing stage name")
		}
		n := fixture()
		n.ID = newID
		n.ImageID = "sha256:target"
		n.Name = "/" + name
		n.State.Running = false
		n.Config.Image = args[len(args)-1]
		n.HostConfig.RestartPolicy.Name = "no"
		f.containers[newID] = &n
		return []byte(newID + "\n"), nil
	case "update":
		d, e := find(args[len(args)-1])
		if e != nil {
			return nil, e
		}
		switch args[1] {
		case "--restart=no":
			d.HostConfig.RestartPolicy.Name = "no"
		case "--restart=unless-stopped":
			d.HostConfig.RestartPolicy.Name = "unless-stopped"
		default:
			return nil, errors.New("unsupported update")
		}
		return []byte("ok"), nil
	case "stop":
		d, e := find(args[len(args)-1])
		if e != nil {
			return nil, e
		}
		d.State.Running = false
		return []byte("stopped"), nil
	case "rename":
		d, e := find(args[1])
		if e != nil {
			return nil, e
		}
		for _, other := range f.containers {
			if other.ID != d.ID && other.Name == "/"+args[2] {
				return nil, errors.New("name in use")
			}
		}
		d.Name = "/" + args[2]
		return []byte("renamed"), nil
	case "start":
		d, e := find(args[1])
		if e != nil {
			return nil, e
		}
		d.State.Running = true
		return []byte("started"), nil
	case "rm":
		d, e := find(args[1])
		if e != nil {
			return nil, e
		}
		if d.State.Running {
			return nil, errors.New("can't remove running agent")
		}
		delete(f.containers, d.ID)
		return []byte("removed"), nil
	default:
		return nil, errors.New("unexpected Docker command")
	}
}

type alwaysDrained struct{}

func (alwaysDrained) CheckDrained(context.Context, string) error { return nil }
func credentialFile(t *testing.T, hostID string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "agent-credentials.json")
	if err := os.WriteFile(name, []byte(`{"hostId":"`+hostID+`","token":"not-logged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func TestAdapterRunsFullDetachedSwapAndPreservesHostID(t *testing.T) {
	cli := newFakeCLI()
	called := false
	target := agentupdate.Target{Image: agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64), Revision: strings.Repeat("d", 40)}
	journalDir := filepath.Join(t.TempDir(), "agent-update-operations")
	a := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute"), OperationID: "test-success", JournalDir: journalDir, Heartbeat: func(_ context.Context, hostID, revision string, after time.Time) error {
		called = true
		if hostID != "host-compute" || revision != target.Revision || after.IsZero() {
			t.Fatalf("heartbeat mismatch %s %s", hostID, revision)
		}
		return nil
	}}
	outcome := agentupdate.Run(context.Background(), agentupdate.Request{HostID: "host-compute", Target: target}, a, alwaysDrained{}, nil)
	if outcome.State != "succeeded" || outcome.Err != nil || !called {
		t.Fatalf("swap failed: %+v heartbeat=%v", outcome, called)
	}
	journal, err := (JournalStore{Dir: journalDir}).Load("test-success")
	if err != nil || journal.State != "awaiting-completion" || journal.OriginalContainerID != oldID || journal.ReplacementContainerID != newID {
		t.Fatalf("recovery journal unavailable before durable server ack: %+v %v", journal, err)
	}
	if err := a.RecordCompletion("succeeded"); err != nil {
		t.Fatal(err)
	}
	journal, err = (JournalStore{Dir: journalDir}).Load("test-success")
	if err != nil || journal.State != "succeeded" {
		t.Fatalf("journal not finalized: %+v %v", journal, err)
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.containers) != 2 || cli.containers[newID] == nil || !cli.containers[newID].State.Running || cli.containers[newID].Name != "/runnerhub-agent" {
		t.Fatalf("unexpected new agent: %+v", cli.containers)
	}
	backup := cli.containers[oldID]
	if backup == nil || backup.State.Running || backup.Name != "/runnerhub-agent-backup-"+oldID[:12] || backup.ImageID != "sha256:old" {
		t.Fatalf("rollback backup was prematurely removed: %+v", backup)
	}
}
func TestAdapterRestoresOriginalWhenNewHeartbeatFails(t *testing.T) {
	cli := newFakeCLI()
	target := agentupdate.Target{Image: agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64), Revision: strings.Repeat("d", 40)}
	journalDir := filepath.Join(t.TempDir(), "agent-update-operations")
	a := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute"), OperationID: "test-rollback", JournalDir: journalDir, Heartbeat: func(context.Context, string, string, time.Time) error { return errors.New("heartbeat missing") }}
	result := agentupdate.Run(context.Background(), agentupdate.Request{HostID: "host-compute", Target: target}, a, alwaysDrained{}, nil)
	if result.State != "failed" || result.Step != "rolled-back" {
		t.Fatalf("rollback not confirmed: %+v", result)
	}
	journal, err := (JournalStore{Dir: journalDir}).Load("test-rollback")
	if err != nil || journal.State != "rolled-back" {
		t.Fatalf("rollback evidence missing: %+v %v", journal, err)
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.containers) != 1 || cli.containers[oldID] == nil || !cli.containers[oldID].State.Running || cli.containers[oldID].Name != "/runnerhub-agent" {
		t.Fatalf("old agent not restored: %+v", cli.containers)
	}
}
func TestCaptureRejectsWrongEnrollmentIDWithoutDockerCommands(t *testing.T) {
	cli := newFakeCLI()
	a := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute")}
	if _, err := a.Capture(context.Background(), "host-other"); err == nil {
		t.Fatal("incorrect host ID accepted")
	}
	if len(cli.calls) != 0 {
		t.Fatalf("Docker was accessed before verifying identity: %v", cli.calls)
	}
}

func TestPulledAgentImageMustMatchApprovedSourceRevision(t *testing.T) {
	cli := newFakeCLI()
	cli.imageRevision = strings.Repeat("e", 40)
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	updater := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute")}
	if err := updater.PullAndVerify(context.Background(), target); err == nil {
		t.Fatal("wrong OCI source revision was accepted")
	}
	cli.imageRevision = target.Revision
	if err := updater.PullAndVerify(context.Background(), target); err != nil {
		t.Fatalf("valid OCI source revision was rejected: %v", err)
	}
}

// Inspect both exact container identities before deleting the replacement.
// An agent whose original container was renamed or modified by an operator
// must not lose its only running agent due to a best-effort rollback.
func TestRestoreDoesNotDeleteHealthyCandidateWhenBackupIdentityChanged(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fakeCLI)
	}{
		{"wrong-backup-name", func(cli *fakeCLI) { cli.containers[oldID].Name = "/runnerhub-agent-backup-unrelated" }},
		{"wrong-backup-image", func(cli *fakeCLI) { cli.containers[oldID].ImageID = "sha256:unexpected" }},
		{"wrong-candidate-name", func(cli *fakeCLI) { cli.containers[newID].Name = "/runnerhub-agent-other" }},
		{"wrong-candidate-image", func(cli *fakeCLI) { cli.containers[newID].Config.Image = "ubuntu:latest" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := newFakeCLI()
			updater := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute"),
				OperationID: "guard-" + tc.name, JournalDir: filepath.Join(t.TempDir(), "journal")}
			target := agentupdate.Target{Image: agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64), Revision: strings.Repeat("d", 40)}
			prior, err := updater.Capture(context.Background(), "host-compute")
			if err != nil {
				t.Fatal(err)
			}
			stage, err := updater.StageReplacement(context.Background(), prior, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := updater.Replace(context.Background(), prior, stage); err != nil {
				t.Fatal(err)
			}
			tc.change(cli)
			if err := updater.Restore(context.Background(), prior, stage); err == nil {
				t.Fatal("unsafe rollback accepted altered container identity")
			}
			candidate := cli.containers[newID]
			if candidate == nil || !candidate.State.Running {
				t.Fatalf("recovery deleted or stopped the healthy replacement before detecting mismatch: %+v", candidate)
			}
		})
	}
}

func TestFinalizeRequiresExactJournaledBackupName(t *testing.T) {
	cli := newFakeCLI()
	updater := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute"),
		OperationID: "finalize-identity", JournalDir: filepath.Join(t.TempDir(), "journal")}
	target := agentupdate.Target{Image: agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64), Revision: strings.Repeat("d", 40)}
	prior, err := updater.Capture(context.Background(), "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := updater.StageReplacement(context.Background(), prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Replace(context.Background(), prior, stage); err != nil {
		t.Fatal(err)
	}
	cli.containers[oldID].Name = "/runnerhub-agent-backup-unrelated"
	if err := updater.Finalize(context.Background(), prior, stage); err == nil {
		t.Fatal("finalization accepted an unrelated backup name")
	}
	if cli.containers[newID] == nil || !cli.containers[newID].State.Running {
		t.Fatal("failed finalization disturbed the healthy replacement")
	}
}
