// runnerhub-agent-updater is the detached Docker host-side executor for a
// single administrator-authorized RunnerHub agent update. It must be started
// as an independent container after a durable lease has been acquired; never
// as an in-process goroutine in runnerhub-agent.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eutelic/runnerhub/internal/agentupdate"
	"github.com/eutelic/runnerhub/internal/agentupdatebridge"
	"github.com/eutelic/runnerhub/internal/agentupdatedocker"
)

func required(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", errors.New(name + " is required")
	}
	return v, nil
}
func run(ctx context.Context) error {
	hostID, err := required("RUNNERHUB_UPDATE_HOST_ID")
	if err != nil {
		return err
	}
	operationID, err := required("RUNNERHUB_UPDATE_OPERATION_ID")
	if err != nil {
		return err
	}
	image, err := required("RUNNERHUB_UPDATE_TARGET_IMAGE")
	if err != nil {
		return err
	}
	revision, err := required("RUNNERHUB_UPDATE_TARGET_REVISION")
	if err != nil {
		return err
	}
	target := agentupdate.Target{Image: image, Revision: revision}
	if err := target.Validate(); err != nil {
		return err
	}
	control, err := required("RUNNERHUB_CONTROL_PLANE_URL")
	if err != nil {
		return err
	}
	cred := os.Getenv("RUNNERHUB_CREDENTIAL_FILE")
	// #nosec G101 -- this literal is a filesystem path, not an embedded credential.
	if cred == "" {
		cred = "/var/lib/runnerhub/agent-credentials.json"
	}
	recoveryOnly := os.Getenv("RUNNERHUB_UPDATE_RECOVERY_ONLY") == "true"
	watchdogOnly := os.Getenv("RUNNERHUB_UPDATE_WATCHDOG") == "true"
	if recoveryOnly && watchdogOnly {
		return errors.New("watchdog and manual recovery modes are mutually exclusive")
	}
	recoveryOwner := ""
	if watchdogOnly {
		recoveryOwner = strings.TrimSpace(os.Getenv("RUNNERHUB_UPDATE_RECOVERY_OWNER"))
		if recoveryOwner == "" {
			return errors.New("watchdog recovery owner must be provisioned by the agent launcher")
		}
	}
	if recoveryOnly {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return errors.New("could not generate exclusive recovery identity")
		}
		recoveryOwner = hex.EncodeToString(nonce[:])
	}
	bridge, err := agentupdatebridge.New(agentupdatebridge.Config{
		ControlURL: control, HostID: hostID, OperationID: operationID,
		TargetImage: image, TargetRevision: revision, CredentialFile: cred, RecoveryOwner: recoveryOwner,
		AllowInsecureHTTP: os.Getenv("RUNNERHUB_ALLOW_INSECURE_HTTP") == "true",
	})
	if err != nil {
		return err
	}
	if watchdogOnly {
		monitor := &agentupdatedocker.Adapter{
			CredentialFile: cred, OperationID: operationID,
			JournalDir: agentupdatedocker.DefaultJournalDir,
		}
		return runWatchdog(ctx, bridge, monitor, hostID, target, watchdogOptions{})
	}
	if recoveryOnly {
		// Recovery is an independent, explicitly requested helper mode.
		// The control plane must still hold the same unresolved operation lease.
		adapter := &agentupdatedocker.Adapter{
			CredentialFile: cred, OperationID: operationID,
			JournalDir: agentupdatedocker.DefaultJournalDir,
		}
		// Check that the original helper has exited before claiming the
		// control-plane recovery fence. RecoverFromJournal rechecks after claim.
		if err := adapter.CheckOriginalUpdaterStopped(ctx, hostID); err != nil {
			return err
		}
		if err := bridge.ClaimRecovery(ctx, hostID); err != nil {
			return err
		}
		recovered, err := adapter.RecoverFromJournal(ctx, hostID, target)
		if err != nil {
			return err
		}
		reportCtx, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		if err := bridge.Report(reportCtx, "rolled-back"); err != nil {
			return errors.New("old agent restored but control plane did not confirm the rolled-back lease")
		}
		log.Printf("recovered old agent for host %s from operation %s, journal state %s", hostID, operationID, recovered.State)
		return nil
	}
	// The agent dispatches the helper before it can acknowledge its launch
	// command. The helper MUST wait for the control plane to durably arm this
	// exact update operation; stopping the old agent before that is unsafe.
	armCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	armed := false
	for !armed {
		if err := bridge.CheckDrained(armCtx, hostID); err == nil {
			armed = true
			break
		}
		select {
		case <-armCtx.Done():
			return errors.New("update helper was not authorized to replace the old agent")
		case <-ticker.C:
		}
	}
	// This is not arbitrary remote shell execution. The adapter only supports
	// the exact pinned RunnerHub agent image and verified bootstrap topology.
	helper := &agentupdatedocker.Adapter{
		CredentialFile: cred, OperationID: operationID,
		JournalDir: agentupdatedocker.DefaultJournalDir, Heartbeat: bridge.WaitForHeartbeat,
	}
	outcome := agentupdate.Run(ctx, agentupdate.Request{
		HostID: hostID, Target: target,
		HeartbeatTimeout: 90 * time.Second, RecoveryTimeout: 2 * time.Minute,
	}, helper, bridge, nil)
	reported := "review"
	if outcome.State == "succeeded" {
		reported = "succeeded"
	}
	if outcome.Step == "rolled-back" {
		reported = "rolled-back"
	}
	// A temporary control-plane outage must not silently masquerade as a
	// completed update. Retry the authenticated terminal report, and otherwise
	// leave the host's durable drain in place for operator review.
	reportCtx, finish := context.WithTimeout(context.Background(), 90*time.Second)
	defer finish()
	reportedOK := false
	for !reportedOK {
		if err := bridge.Report(reportCtx, reported); err == nil {
			reportedOK = true
			break
		}
		select {
		case <-reportCtx.Done():
			return errors.New("agent update outcome could not be durably recorded; host remains draining")
		case <-time.After(3 * time.Second):
		}
	}
	if outcome.State != "succeeded" {
		// The journal already records any confirmed rollback. If the updater
		// outcome is ambiguous, mark the retained backup for manual inspection.
		if reported == "review" {
			_ = helper.RecordCompletion("review")
		}
		log.Printf("agent update did not succeed: state=%s step=%s", outcome.State, outcome.Step)
		return errors.New("agent update incomplete; host remains draining for review")
	}
	// Only the server's durable HTTP 200 completion authorizes the journal's
	// terminal state. The old stopped backup remains available for explicit
	// post-acknowledgment cleanup or operator recovery.
	if err := helper.RecordCompletion("succeeded"); err != nil {
		return errors.New("agent update succeeded but local recovery journal could not be finalized")
	}
	log.Printf("updated agent heartbeat and control-plane completion verified for host ID %s", hostID)
	return nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
