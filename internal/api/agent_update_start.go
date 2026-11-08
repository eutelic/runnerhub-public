package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agentupdategate"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

var officialUpdaterDigest = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent-updater@sha256:[a-f0-9]{64}$`)

// POST /api/v1/hosts/{id}/update-agent starts a durable, host-specific
// rolling upgrade. It is disabled unless an operator explicitly configured
// a pinned, verified target and a pinned helper image. No SSH is involved.
func (s *Service) updateAgent(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("agent update requires admin access"))
		return
	}
	if !s.AgentUpdateEnabled {
		problem(w, http.StatusServiceUnavailable, errors.New("agent update feature is not enabled on this control plane"))
		return
	}
	if s.Hub == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("agent command channel is unavailable"))
		return
	}
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("durable per-host update operations unavailable"))
		return
	}
	h, err := s.Store.GetHost(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, err)
		} else {
			problem(w, http.StatusInternalServerError, errors.New("host lookup failed"))
		}
		return
	}
	// A repeated click during the same draining/replacing operation must
	// return its durable identity rather than fail or create a second helper.
	if h.State == core.HostDraining {
		current, err := leases.GetAgentUpdateLease(r.Context(), h.ID)
		if err == nil && (current.State == "draining" || current.State == "dispatching" || current.State == "replacing") {
			writeJSON(w, http.StatusAccepted, map[string]any{"operation": current, "reused": true})
			return
		}
		problem(w, http.StatusConflict, errors.New("host is draining and no safe reusable agent update exists"))
		return
	}
	if !officialUpdaterDigest.MatchString(s.AgentUpdateHelperImage) {
		problem(w, http.StatusServiceUnavailable, errors.New("configured helper image is not an immutable official digest"))
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, errors.New("cannot inspect active host work"))
		return
	}
	pre := makeAgentUpdatePreflight(h, workers, s.AgentUpdateTargetImage, s.AgentUpdateTargetRevision)
	if h.Provider != "docker" || h.State != core.HostOnline || !pre.TargetMetadataValid || !pre.UpdaterAdvertised {
		problem(w, http.StatusConflict, errors.New("host or published agent update target is not eligible"))
		return
	}
	if h.AgentVersion == s.AgentUpdateTargetRevision {
		problem(w, http.StatusConflict, errors.New("host already advertises the requested release revision"))
		return
	}
	lease := store.AgentUpdateLease{
		HostID: h.ID, OperationID: "update-" + randomID(),
		TargetImage: s.AgentUpdateTargetImage, TargetRevision: s.AgentUpdateTargetRevision,
		HelperImage: s.AgentUpdateHelperImage,
		Actor:       actor(r.Context()),
	}
	created, reused, err := leases.BeginAgentUpdateLease(r.Context(), lease)
	if err != nil {
		if errors.Is(err, store.ErrAgentUpdateBusy) {
			if created.State == "draining" || created.State == "dispatching" || created.State == "replacing" {
				writeJSON(w, http.StatusAccepted, map[string]any{"operation": created, "reused": true})
				return
			}
			problem(w, http.StatusConflict, errors.New("host has an unresolved update requiring operator review"))
			return
		}
		problem(w, http.StatusConflict, errors.New("cannot start safe host drain"))
		return
	}
	if !reused {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
			Actor: actor(r.Context()), Action: "start_agent_update", ResourceType: "host",
			ResourceID: h.ID, Result: "draining", SafeDetail: "update operation accepted; waiting for existing workers",
		})
		// #nosec G118 -- detached maintenance has a bounded four-hour lifetime, independent of the request.
		go s.performHostAgentUpdate(created)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"operation": created, "reused": reused})
}

func (s *Service) performHostAgentUpdate(lease store.AgentUpdateLease) {
	// #nosec G118 -- bounded, intentionally detached host-specific maintenance
	// worker; all mutable state is in SQLite, not the request context.
	if !s.AgentUpdateEnabled || s.Hub == nil || !officialUpdaterDigest.MatchString(lease.HelperImage) ||
		lease.HelperImage != s.AgentUpdateHelperImage ||
		lease.TargetImage != s.AgentUpdateTargetImage ||
		lease.TargetRevision != s.AgentUpdateTargetRevision {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		return
	}
	if lease.State == "draining" {
		gate := agentupdategate.Gate{Store: s.Store}
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			if err := gate.CheckDrained(ctx, lease.HostID); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				_ = s.failAgentUpdateDrain(lease, "drain-timed-out")
				return
			case <-ticker.C:
			}
		}
		if err := leases.TransitionAgentUpdateLease(ctx, lease.HostID, lease.OperationID, "draining", "dispatching", "dispatching-helper"); err != nil {
			return
		}
	} else if lease.State != "dispatching" {
		return
	}
	cmd := agent.Command{
		Type: agent.CommandUpdateAgent, WorkerID: lease.OperationID, Provider: "docker",
		AgentUpdate: &agent.AgentUpdateSpec{
			HostID: lease.HostID, OperationID: lease.OperationID,
			TargetImage: lease.TargetImage, TargetRevision: lease.TargetRevision,
			HelperImage: lease.HelperImage,
		},
	}
	if err := s.Hub.Enqueue(lease.HostID, cmd); err != nil {
		_ = leases.TransitionAgentUpdateLease(context.Background(), lease.HostID, lease.OperationID, "dispatching", "review", "helper-command-could-not-be-persisted")
	}
}
func (s *Service) failAgentUpdateDrain(lease store.AgentUpdateLease, step string) error {
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		return errors.New("agent update lease store unavailable")
	}
	return leases.TransitionAgentUpdateLease(context.Background(), lease.HostID, lease.OperationID, "draining", "review", step)
}

// RecoverAgentUpdates is called once at control-plane startup. The durable
// operation is resumed only when its exact pinned target still matches the
// explicitly enabled configuration. Unknown and review states never retry.
func (s *Service) RecoverAgentUpdates(ctx context.Context) error {
	if !s.AgentUpdateEnabled || s.Hub == nil {
		return nil
	}
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		return errors.New("agent update recovery store unavailable")
	}
	active, err := leases.ListActiveAgentUpdateLeases(ctx)
	if err != nil {
		return err
	}
	for _, lease := range active {
		if (lease.State == "draining" || lease.State == "dispatching") &&
			lease.HelperImage == s.AgentUpdateHelperImage &&
			lease.TargetImage == s.AgentUpdateTargetImage &&
			lease.TargetRevision == s.AgentUpdateTargetRevision {
			// #nosec G118 -- startup recovery resumes a durable operation with a bounded worker context.
			go s.performHostAgentUpdate(lease)
		}
		// A replacing helper must finish itself, not be dispatched a second time.
	}
	return nil
}
