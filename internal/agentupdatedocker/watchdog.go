package agentupdatedocker

import (
	"context"
	"encoding/json"
	"errors"
)

// WatchdogJournal reads only the nonsecret exact-Docker-ID recovery record.
// The watchdog must never guess a container from an image tag or name.
func (a *Adapter) WatchdogJournal() (RecoveryRecord, error) {
	if a.OperationID == "" {
		return RecoveryRecord{}, errors.New("watchdog operation ID required")
	}
	return a.journal().Load(a.OperationID)
}

// RetireWatchdog disables the watchdog's restart policy before the monitor
// exits. This prevents an endless restart loop after a terminal operation.
// The existing container is retained for audit and explicit later cleanup.
func (a *Adapter) RetireWatchdog(ctx context.Context, hostID string) error {
	if hostID == "" || !safeOperationID.MatchString(a.OperationID) {
		return errors.New("exact watchdog identity required")
	}
	name := "runnerhub-agent-watchdog-" + a.OperationID
	raw, err := a.client().Run(ctx, "inspect", name)
	if err != nil {
		return errors.New("watchdog container cannot be verified")
	}
	var found []helperInspect
	if json.Unmarshal(raw, &found) != nil || len(found) != 1 {
		return errors.New("ambiguous watchdog identity")
	}
	w := found[0]
	if !exactContainerID.MatchString(w.ID) || !w.State.Running ||
		!updaterImagePattern.MatchString(w.Config.Image) ||
		w.Config.Labels["io.runnerhub.update.host"] != hostID ||
		w.Config.Labels["io.runnerhub.update.operation"] != a.OperationID ||
		w.Config.Labels["io.runnerhub.update.role"] != "watchdog" {
		return errors.New("watchdog identity or state mismatch")
	}
	if _, err := a.client().Run(ctx, "update", "--restart=no", w.ID); err != nil {
		return errors.New("could not disable completed watchdog restart")
	}
	return nil
}
