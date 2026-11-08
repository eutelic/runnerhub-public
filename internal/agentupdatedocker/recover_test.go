package agentupdatedocker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/agentupdate"
)

// A recovery helper must inspect the *original* updater rather than assume
// that a control-plane lease or old process disappearance means it exited.
func stoppedOriginalHelper(cli *fakeCLI, operationID, hostID string) {
	helperID := strings.Repeat("e", 64)
	h := fixture()
	h.ID = helperID
	h.Name = "/runnerhub-agent-updater-" + operationID
	h.State.Running = false
	h.Config.Image = updaterRepository + "@sha256:" + strings.Repeat("f", 64)
	h.Config.Labels = map[string]string{
		"io.runnerhub.update.host":      hostID,
		"io.runnerhub.update.operation": operationID,
		"io.runnerhub.update.role":      "updater",
	}
	cli.containers[helperID] = &h
}

func TestJournalRecoveryAfterDetachedHelperCrashesMidSwap(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	dir := filepath.Join(t.TempDir(), "agent-update-operations")
	cred := credentialFile(t, "host-compute")
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	original := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-1", JournalDir: dir}
	prior, err := original.Capture(ctx, "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	if err := original.PullAndVerify(ctx, target); err != nil {
		t.Fatal(err)
	}
	stage, err := original.StageReplacement(ctx, prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Replace(ctx, prior, stage); err != nil {
		t.Fatal(err)
	}
	stoppedOriginalHelper(cli, "crash-1", "host-compute")
	// Simulate process death: discard all in-memory prior/stage identity maps.
	original = nil
	recorder := JournalStore{Dir: dir}
	interrupted, err := recorder.Load("crash-1")
	if err != nil || interrupted.State != "replacing" {
		t.Fatalf("no pre-crash recovery evidence: %+v %v", interrupted, err)
	}
	rescue := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-1", JournalDir: dir}
	recovered, err := rescue.RecoverFromJournal(ctx, "host-compute", target)
	if err != nil || recovered.State != "rolled-back" {
		t.Fatalf("recovery failed: %+v %v", recovered, err)
	}
	cli.mu.Lock()
	if len(cli.containers) != 2 || cli.containers[oldID] == nil ||
		cli.containers[oldID].Name != "/runnerhub-agent" || !cli.containers[oldID].State.Running {
		t.Fatalf("exact old agent was not restored: %+v", cli.containers)
	}
	cli.mu.Unlock()
}

func TestJournalRecoveryRejectsChangedReleaseAndCompletedUpdate(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	dir := filepath.Join(t.TempDir(), "agent-update-operations")
	cred := credentialFile(t, "host-compute")
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	updater := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-2", JournalDir: dir}
	prior, err := updater.Capture(ctx, "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := updater.StageReplacement(ctx, prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Replace(ctx, prior, stage); err != nil {
		t.Fatal(err)
	}
	stoppedOriginalHelper(cli, "crash-2", "host-compute")
	rescue := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-2", JournalDir: dir}
	mismatch := target
	mismatch.Revision = strings.Repeat("f", 40)
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", mismatch); err == nil {
		t.Fatal("unapproved target revision was accepted for rollback")
	}
	if cli.containers[newID] == nil || !cli.containers[newID].State.Running {
		t.Fatal("mismatched recovery mutated the running replacement")
	}
	if err := updater.RecordCompletion("succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("completed update was rolled back without new authorization")
	}
}

func TestJournalRecoveryRejectsLiveOrMismatchedOriginalUpdater(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	dir := filepath.Join(t.TempDir(), "agent-update-operations")
	cred := credentialFile(t, "host-compute")
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	updater := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-3", JournalDir: dir}
	prior, err := updater.Capture(ctx, "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := updater.StageReplacement(ctx, prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Replace(ctx, prior, stage); err != nil {
		t.Fatal(err)
	}
	rescue := &Adapter{Docker: cli, CredentialFile: cred, OperationID: "crash-3", JournalDir: dir}
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("missing original helper was treated as safely stopped")
	}
	stoppedOriginalHelper(cli, "crash-3", "host-compute")
	helperID := strings.Repeat("e", 64)
	cli.containers[helperID].State.Running = true
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("recovered while the original update helper was still running")
	}
	cli.containers[helperID].State.Running = false
	cli.containers[helperID].Config.Labels["io.runnerhub.update.operation"] = "another-operation"
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("accepted unrelated helper identity")
	}
	cli.containers[helperID].Config.Labels["io.runnerhub.update.operation"] = "crash-3"
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err != nil {
		t.Fatalf("verified stopped helper should allow exact rollback: %v", err)
	}
}

// Durable journal state alone cannot confirm a restored agent survived a
// subsequent Docker restart; the exact original container must be running
// under its canonical name with the expected restart policy.
func TestJournalRecoveryRechecksAlreadyRolledBackDockerState(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	dir := filepath.Join(t.TempDir(), "agent-update-operations")
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	operationID := "crash-rolledback"
	credentials := credentialFile(t, "host-compute")
	updater := &Adapter{Docker: cli, CredentialFile: credentials, OperationID: operationID, JournalDir: dir}
	prior, err := updater.Capture(ctx, "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := updater.StageReplacement(ctx, prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Replace(ctx, prior, stage); err != nil {
		t.Fatal(err)
	}
	stoppedOriginalHelper(cli, operationID, "host-compute")
	rescue := &Adapter{Docker: cli, CredentialFile: credentials, OperationID: operationID, JournalDir: dir}
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err != nil {
		t.Fatal(err)
	}
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err != nil {
		t.Fatalf("verified physical rollback should be idempotent: %v", err)
	}
	original := cli.containers[oldID]
	original.State.Running = false
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("stopped original agent was accepted using only the rolled-back journal")
	}
	original.State.Running = true
	original.Name = "/runnerhub-agent-backup-" + oldID[:12]
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("running original under backup name was accepted as restored")
	}
	original.Name = "/runnerhub-agent"
	original.HostConfig.RestartPolicy.Name = "no"
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("restored agent with disabled restart policy was accepted")
	}
	original.HostConfig.RestartPolicy.Name = "unless-stopped"
	original.ImageID = "sha256:changed"
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
		t.Fatal("different image was accepted as the original agent")
	}
	original.ImageID = prior.Image
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err != nil {
		t.Fatalf("restored original was not confirmed after repairing Docker state: %v", err)
	}
}

// A helper crash can happen after it deletes the failed replacement but
// before it restores the previous agent. Docker's authoritative inventory
// must prove the exact replacement absent before journal recovery continues.
func TestRecoveryCompletesRollbackWhenCandidateAlreadyRemoved(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	dir := filepath.Join(t.TempDir(), "agent-update-operations")
	credential := credentialFile(t, "host-compute")
	operationID := "crash-missing-candidate"
	target := agentupdate.Target{
		Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
		Revision: strings.Repeat("d", 40),
	}
	original := &Adapter{Docker: cli, CredentialFile: credential, OperationID: operationID, JournalDir: dir}
	prior, err := original.Capture(ctx, "host-compute")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := original.StageReplacement(ctx, prior, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Replace(ctx, prior, stage); err != nil {
		t.Fatal(err)
	}
	if err := original.writeRecoveryState("rolling-back"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Run(ctx, "stop", "--time", "30", stage.ContainerID); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Run(ctx, "rm", stage.ContainerID); err != nil {
		t.Fatal(err)
	}
	stoppedOriginalHelper(cli, operationID, "host-compute")
	rescue := &Adapter{Docker: cli, CredentialFile: credential, OperationID: operationID, JournalDir: dir}
	recovered, err := rescue.RecoverFromJournal(ctx, "host-compute", target)
	if err != nil || recovered.State != "rolled-back" {
		t.Fatalf("orphaned backup was not recovered: %+v %v", recovered, err)
	}
	old := cli.containers[oldID]
	if old == nil || !old.State.Running || old.Name != "/runnerhub-agent" ||
		old.ImageID != prior.Image || old.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Fatalf("failed to restore verified original agent: %+v", old)
	}
	if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err != nil {
		t.Fatalf("completed rollback should remain idempotent: %v", err)
	}
}

func TestMissingCandidateCannotBypassEarlyJournalOrBrokenDocker(t *testing.T) {
	ctx := context.Background()
	for _, journalState := range []string{"staged", "replacing", "rolling-back"} {
		t.Run(journalState, func(t *testing.T) {
			cli := newFakeCLI()
			dir := filepath.Join(t.TempDir(), "operations")
			target := agentupdate.Target{
				Image:    agentupdate.AgentImage + "@sha256:" + strings.Repeat("c", 64),
				Revision: strings.Repeat("d", 40),
			}
			id := "crash-in-" + journalState
			u := &Adapter{Docker: cli, CredentialFile: credentialFile(t, "host-compute"), OperationID: id, JournalDir: dir}
			prior, err := u.Capture(ctx, "host-compute")
			if err != nil {
				t.Fatal(err)
			}
			stage, err := u.StageReplacement(ctx, prior, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := u.Replace(ctx, prior, stage); err != nil {
				t.Fatal(err)
			}
			if err := u.writeRecoveryState(journalState); err != nil {
				t.Fatal(err)
			}
			if _, err := cli.Run(ctx, "stop", "--time", "30", stage.ContainerID); err != nil {
				t.Fatal(err)
			}
			if _, err := cli.Run(ctx, "rm", stage.ContainerID); err != nil {
				t.Fatal(err)
			}
			stoppedOriginalHelper(cli, id, "host-compute")
			rescue := &Adapter{Docker: cli, CredentialFile: u.CredentialFile, OperationID: id, JournalDir: dir}
			if journalState != "rolling-back" {
				if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
					t.Fatal("missing replacement was accepted without a rollback journal")
				}
				if cli.containers[oldID].State.Running {
					t.Fatal("guard failure mutated the original host")
				}
				return
			}
			cli.fail = "ps"
			if _, err := rescue.RecoverFromJournal(ctx, "host-compute", target); err == nil {
				t.Fatal("Docker inventory failure was mistaken for verified replacement deletion")
			}
			if cli.containers[oldID].State.Running {
				t.Fatal("Docker inventory failure mutated the original host")
			}
		})
	}
}
