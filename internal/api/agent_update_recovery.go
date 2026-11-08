package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/eutelic/runnerhub/internal/store"
)

type agentUpdateRecoveryRequest struct {
	HostID        string `json:"hostId"`
	OperationID   string `json:"operationId"`
	RecoveryOwner string `json:"recoveryOwner"`
}

// A single recovery helper must claim the exact host/operation atomically
// before any Docker changes. The same owner may retry after a lost response,
// but a second helper cannot share an already-claimed recovery lease.
func (s *Service) agentUpdateBeginRecovery(w http.ResponseWriter, r *http.Request) {
	var in agentUpdateRecoveryRequest
	if !decode(w, r, &in) {
		return
	}
	in.HostID = strings.TrimSpace(in.HostID)
	in.OperationID = strings.TrimSpace(in.OperationID)
	in.RecoveryOwner = strings.TrimSpace(in.RecoveryOwner)
	if s.Hub == nil || in.HostID == "" || in.OperationID == "" ||
		!s.Hub.Authenticate(in.HostID, bearer(r)) {
		unauthorizedAgent(w)
		return
	}
	if len(in.RecoveryOwner) != 32 || strings.Trim(in.RecoveryOwner, "0123456789abcdef") != "" {
		problem(w, http.StatusBadRequest, errors.New("a 128-bit recovery owner is required"))
		return
	}
	leases, ok := s.Store.(store.AgentUpdateLeaseStore)
	if !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("durable update operations unavailable"))
		return
	}
	lease, err := leases.ClaimAgentUpdateRecovery(r.Context(), in.HostID, in.OperationID, in.RecoveryOwner)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			problem(w, http.StatusNotFound, errors.New("recovery operation not found"))
		case errors.Is(err, store.ErrAgentUpdateChanged):
			problem(w, http.StatusConflict, errors.New("recovery is already owned or operation is not replaceable"))
		default:
			problem(w, http.StatusInternalServerError, errors.New("recovery state could not be persisted"))
		}
		return
	}
	writeJSON(w, http.StatusOK, lease)
}
