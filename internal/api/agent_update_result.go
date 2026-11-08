package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

type agentUpdateResultRequest struct {
	HostID        string `json:"hostId"`
	OperationID   string `json:"operationId"`
	Outcome       string `json:"outcome"`
	RecoveryOwner string `json:"recoveryOwner,omitempty"`
}

// A helper reporting "succeeded" cannot force a host online. The SQLite
// terminal transition independently verifies new image revision heartbeat
// and absence of reserved CI worker/cleanup resources before resuming.
func (s *Service) agentUpdateResult(w http.ResponseWriter, r *http.Request) {
	var in agentUpdateResultRequest
	if !decode(w, r, &in) {
		return
	}
	in.HostID = strings.TrimSpace(in.HostID)
	in.OperationID = strings.TrimSpace(in.OperationID)
	if s.Hub == nil || in.HostID == "" || in.OperationID == "" ||
		!s.Hub.Authenticate(in.HostID, bearer(r)) {
		unauthorizedAgent(w)
		return
	}
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("agent update persistence unavailable"))
		return
	}
	if in.Outcome != "succeeded" && in.Outcome != "rolled-back" && in.Outcome != "review" {
		problem(w, http.StatusBadRequest, errors.New("unsupported agent update outcome"))
		return
	}
	l, err := leases.CompleteAgentUpdateLease(r.Context(), in.HostID, in.OperationID, in.Outcome, in.RecoveryOwner)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, errors.New("update lease not found"))
		} else if errors.Is(err, store.ErrAgentUpdateChanged) {
			problem(w, http.StatusConflict, errors.New("agent update status not safely verifiable"))
		} else {
			problem(w, http.StatusInternalServerError, errors.New("agent update completion failed"))
		}
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: "agent:" + in.HostID, Action: "agent_update_result",
		ResourceType: "host", ResourceID: in.HostID, Result: l.State, SafeDetail: l.Step,
	})
	writeJSON(w, http.StatusOK, l)
}
