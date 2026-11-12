package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/fleet"
	"github.com/eutelic/runnerhub/internal/store"
)

const fleetAlertSustainment = 5 * time.Minute
const listenerStatusFreshness = 2 * time.Minute

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "unknown"
}

func fleetPoolRepository(pool core.RunnerPool) string {
	owner, repository := strings.TrimSpace(pool.Scope.Owner), strings.TrimSpace(pool.Scope.Repository)
	if owner == "" || repository == "" {
		return ""
	}
	return owner + "/" + repository
}

type fleetAlertCandidate struct {
	ID       string
	Title    string
	Detail   string
	Href     string
	Severity string
	Cause    string
	Type     string
	EntityID string
	Repo     string
}

// RunFleetAlertMonitor continuously samples durable control-plane state. It
// does not depend on a dashboard tab being open.
func (s *Service) RunFleetAlertMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.reconcileFleetAlerts(ctx); err != nil && ctx.Err() == nil {
			log.Printf("fleet alert sampler: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) reconcileFleetAlerts(ctx context.Context) error {
	return s.reconcileFleetAlertsAt(ctx, time.Now().UTC())
}

func (s *Service) reconcileFleetAlertsAt(ctx context.Context, now time.Time) error {
	// Serialize sampling with operator actions so stale snapshots cannot erase
	// acknowledgments or mutes, or overwrite a newer incident state.
	s.fleetAlertMu.Lock()
	defer s.fleetAlertMu.Unlock()
	alerts, ok := s.Store.(store.FleetAlertStore)
	if !ok {
		return nil
	}
	hosts, err := s.Store.ListHosts(ctx)
	if err != nil {
		return err
	}
	pools, err := s.Store.ListPools(ctx)
	if err != nil {
		return err
	}
	workers, err := s.Store.ListWorkers(ctx)
	if err != nil {
		return err
	}
	maintenance, err := s.maintenanceState(ctx)
	if err != nil {
		return err
	}
	s.scaleSetStatusMu.RLock()
	statuses := make([]scaleSetStatusResponse, 0, len(s.scaleSetStatuses))
	for _, status := range s.scaleSetStatuses {
		statuses = append(statuses, status)
	}
	s.scaleSetStatusMu.RUnlock()
	candidates := detectServerFleetAlerts(hosts, pools, workers, statuses, maintenance.Paused, now)
	current := make(map[string]fleetAlertCandidate, len(candidates))
	for _, candidate := range candidates {
		current[candidate.ID] = candidate
		previous, getErr := alerts.GetFleetAlert(ctx, candidate.ID)
		if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
			return getErr
		}
		if errors.Is(getErr, store.ErrNotFound) || previous.State == "resolved" {
			previous = store.FleetAlert{ID: candidate.ID, FirstSeenAt: now}
		}
		state := "observing"
		if !now.Before(previous.FirstSeenAt.Add(fleetAlertSustainment)) {
			state = "active"
		}
		previous.Title = candidate.Title
		previous.Detail = candidate.Detail
		previous.Href = candidate.Href
		previous.Severity = candidate.Severity
		previous.Cause = candidate.Cause
		previous.AffectedType = candidate.Type
		previous.AffectedID = candidate.EntityID
		previous.Repository = candidate.Repo
		previous.State = state
		previous.LastSeenAt = now
		previous.RecoveryStartedAt = time.Time{}
		previous.ResolvedAt = time.Time{}
		if err := alerts.UpsertFleetAlert(ctx, previous); err != nil {
			return err
		}
	}
	existing, err := alerts.ListOpenFleetAlerts(ctx)
	if err != nil {
		return err
	}
	for _, previous := range existing {
		if _, present := current[previous.ID]; present || previous.State == "resolved" {
			continue
		}
		if listenerAlertSignalUnknown(previous, pools, statuses, maintenance.Paused, now) {
			// A control-plane restart clears the process-local listener snapshot.
			// Missing/stale telemetry cannot prove recovery; cancel any tentative
			// recovery window and keep the durable incident open until a fresh
			// listener observation can confirm health or removal.
			if previous.State == "resolving" {
				previous.State = "active"
				previous.RecoveryStartedAt = time.Time{}
				if err := alerts.UpsertFleetAlert(ctx, previous); err != nil {
					return err
				}
			}
			continue
		}
		switch previous.State {
		case "observing":
			previous.State = "resolved"
			previous.ResolvedAt = now
		case "active":
			previous.State = "resolving"
			previous.RecoveryStartedAt = now
		case "resolving":
			if !now.Before(previous.RecoveryStartedAt.Add(fleetAlertSustainment)) {
				previous.State = "resolved"
				previous.ResolvedAt = now
			}
		default:
			previous.State = "resolved"
			previous.ResolvedAt = now
		}
		if err := alerts.UpsertFleetAlert(ctx, previous); err != nil {
			return err
		}
	}
	return nil
}

func listenerAlertSignalUnknown(alert store.FleetAlert, pools []core.RunnerPool, statuses []scaleSetStatusResponse, maintenancePaused bool, now time.Time) bool {
	if maintenancePaused || (alert.Cause != "listener_error" && alert.Cause != "listener_heartbeat_stale" && alert.Cause != "no_ready_listeners") {
		return false
	}
	statusByPool := make(map[string]scaleSetStatusResponse, len(statuses))
	for _, status := range statuses {
		statusByPool[status.PoolID] = status
	}
	if alert.Cause == "listener_error" || alert.Cause == "listener_heartbeat_stale" {
		for _, pool := range pools {
			if pool.ID != alert.AffectedID || pool.ScaleSetDisabled || pool.GitHubInstallationID <= 0 {
				continue
			}
			status, exists := statusByPool[pool.ID]
			return !exists || status.UpdatedAt.IsZero() || now.Sub(status.UpdatedAt) > listenerStatusFreshness
		}
		// A removed or disconnected pool no longer needs a listener recovery signal.
		return false
	}

	connected := 0
	unknown := false
	for _, pool := range pools {
		if pool.ScaleSetDisabled || pool.GitHubInstallationID <= 0 {
			continue
		}
		connected++
		status, exists := statusByPool[pool.ID]
		fresh := exists && !status.UpdatedAt.IsZero() && now.Sub(status.UpdatedAt) <= listenerStatusFreshness
		if fresh && status.State == "ready" {
			// Any healthy listener disproves the fleet-wide no-ready condition,
			// even if an earlier pool has missing or stale telemetry.
			return false
		}
		if !fresh || status.State == "disabled" {
			unknown = true
		}
	}
	// Missing/stale telemetry for any connected pool cannot prove recovery.
	return connected > 0 && unknown
}

func detectServerFleetAlerts(hosts []core.Host, pools []core.RunnerPool, workers []core.Worker, statuses []scaleSetStatusResponse, maintenancePaused bool, now time.Time) []fleetAlertCandidate {
	if maintenancePaused {
		return nil
	}
	var out []fleetAlertCandidate
	for _, host := range hosts {
		if host.State != core.HostOffline || host.LastSeenAt.IsZero() || now.Sub(host.LastSeenAt) < fleetAlertSustainment {
			continue
		}
		out = append(out, fleetAlertCandidate{
			ID: "host:" + host.ID, Title: "Host " + firstNonBlank(host.Name, host.ID) + " offline",
			Detail: "No agent heartbeat has been received for at least five minutes. Check host connectivity and agent diagnostics.",
			Href:   "/diagnostics.html", Severity: "warning", Cause: "host_offline", Type: "host", EntityID: host.ID,
		})
	}
	statusByPool := make(map[string]scaleSetStatusResponse, len(statuses))
	ready := 0
	connected := 0
	allStatusesKnown := true
	for _, status := range statuses {
		statusByPool[status.PoolID] = status
	}
	for _, pool := range pools {
		if pool.ScaleSetDisabled || pool.GitHubInstallationID <= 0 {
			continue
		}
		connected++
		status, exists := statusByPool[pool.ID]
		if !exists || status.State == "disabled" {
			allStatusesKnown = false
		}
		statusFresh := exists && !status.UpdatedAt.IsZero() && now.Sub(status.UpdatedAt) <= listenerStatusFreshness
		if statusFresh && status.State == "ready" {
			ready++
		}
		staleReady := exists && status.State == "ready" && !statusFresh
		if !maintenancePaused && exists && (status.State == "error" || status.State == "backing_off" || staleReady) {
			cause := "listener_error"
			title := "Listener " + pool.Name + " " + status.State
			detail := "The scale-set listener reports a sustained error or retry backoff. Check listener authentication, GitHub API access, and diagnostics."
			if staleReady {
				cause = "listener_heartbeat_stale"
				title = "Listener " + pool.Name + " is not responding"
				detail = "The listener has not reported status for more than two minutes. Check the scale-set process and control-plane connectivity."
			}
			out = append(out, fleetAlertCandidate{
				ID: "listener:" + pool.ID, Title: title,
				Detail: detail,
				Href:   "/diagnostics.html", Severity: "warning", Cause: cause, Type: "pool", EntityID: pool.ID,
				Repo: fleetPoolRepository(pool),
			})
		}
	}
	if !maintenancePaused && connected > 0 && ready == 0 && allStatusesKnown {
		out = append(out, fleetAlertCandidate{
			ID: "listeners:none-ready", Title: "No connected scale-set listeners are ready",
			Detail: "All enabled connected pools have reported listener state, but none is ready. Check listener diagnostics and GitHub access.",
			Href:   "/diagnostics.html", Severity: "critical", Cause: "no_ready_listeners", Type: "fleet", EntityID: "listeners",
		})
	}
	if !maintenancePaused {
		connectedPools := map[string]struct{}{}
		for _, pool := range pools {
			if !pool.ScaleSetDisabled && pool.GitHubInstallationID > 0 {
				connectedPools[pool.ID] = struct{}{}
			}
		}
		for _, health := range fleet.RuntimeHealthByPool(pools, workers, now) {
			if _, connected := connectedPools[health.PoolID]; !connected {
				continue
			}
			if health.State != "degraded" || health.LastFailureAt.IsZero() || health.LastFailureAt.Before(now.Add(-24*time.Hour)) {
				continue
			}
			poolName := health.PoolID
			for _, pool := range pools {
				if pool.ID == health.PoolID {
					poolName = firstNonBlank(pool.Name, pool.ID)
					break
				}
			}
			var repository string
			for _, pool := range pools {
				if pool.ID == health.PoolID {
					repository = fleetPoolRepository(pool)
					break
				}
			}
			out = append(out, fleetAlertCandidate{
				ID: "runtime:" + health.PoolID, Title: "Runtime " + poolName + " degraded",
				Detail: "Recent worker failure evidence is newer than the last successful job. Review sanitized runner diagnostics.",
				Href:   "/diagnostics.html", Severity: "warning", Cause: "runtime_degraded", Type: "pool", EntityID: health.PoolID, Repo: repository,
			})
		}
	}
	failures := map[string]int{}
	for _, worker := range workers {
		if worker.JobName != "" || worker.JobResult != "" || worker.WorkflowRunID != 0 || worker.State != core.WorkerFailed || worker.FailureReason == "" {
			continue
		}
		at := worker.FailedAt
		if at.IsZero() {
			at = worker.UpdatedAt
		}
		if at.IsZero() || at.Before(now.Add(-30*time.Minute)) || at.After(now) {
			continue
		}
		failures[worker.PoolID]++
	}
	cleanupPending := map[string]int{}
	for _, worker := range workers {
		if worker.State != core.WorkerCleanupPending || strings.TrimSpace(worker.PoolID) == "" {
			continue
		}
		at := worker.UpdatedAt
		if at.IsZero() {
			at = worker.FailedAt
		}
		if at.IsZero() {
			at = worker.CompletedAt
		}
		if at.IsZero() || at.After(now) || now.Sub(at) < fleetAlertSustainment {
			continue
		}
		cleanupPending[worker.PoolID]++
	}
	for poolID, count := range cleanupPending {
		if count < 3 {
			continue
		}
		out = append(out, fleetAlertCandidate{
			ID: "cleanup:" + poolID, Title: "Repeated worker cleanup backlog",
			Detail: "At least three workers have remained in cleanup_pending without a fresh update for more than five minutes. Check provider delete retries and host connectivity.",
			Href:   "/diagnostics.html", Severity: "warning", Cause: "worker_cleanup_backlog", Type: "pool", EntityID: poolID,
		})
	}
	for poolID, count := range failures {
		if count < 3 {
			continue
		}
		out = append(out, fleetAlertCandidate{
			ID: "startup:" + poolID, Title: "Repeated runner startup failures",
			Detail: "At least three runner startups failed before a GitHub job began in the last 30 minutes. Review provisioning and registration diagnostics.",
			Href:   "/diagnostics.html", Severity: "warning", Cause: "runner_startup_failures", Type: "pool", EntityID: poolID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Service) fleetAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, ok := s.Store.(store.FleetAlertStore)
	if !ok {
		writeJSON(w, http.StatusOK, []store.FleetAlert{})
		return
	}
	items, err := alerts.ListFleetAlerts(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Service) updateFleetAlert(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("admin access required to change fleet alerts"))
		return
	}
	alerts, ok := s.Store.(store.FleetAlertStore)
	if !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("durable fleet alerts are unavailable"))
		return
	}
	s.fleetAlertMu.Lock()
	defer s.fleetAlertMu.Unlock()
	id := strings.TrimSpace(r.PathValue("id"))
	alert, err := alerts.GetFleetAlert(r.Context(), id)
	if err != nil {
		problem(w, http.StatusNotFound, errors.New("fleet alert not found"))
		return
	}
	now := time.Now().UTC()
	switch r.PathValue("action") {
	case "acknowledge":
		alert.AcknowledgedAt = now
	case "mute":
		alert.MutedUntil = now.Add(time.Hour)
	case "unmute":
		alert.MutedUntil = time.Time{}
	default:
		http.NotFound(w, r)
		return
	}
	if err := alerts.UpsertFleetAlert(r.Context(), alert); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "fleet_alert_" + r.PathValue("action"), ResourceType: "fleet_alert", ResourceID: alert.ID, Result: "ok"})
	writeJSON(w, http.StatusOK, alert)
}
