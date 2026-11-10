package api

import (
	"errors"
	"net/http"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

type hostSchedulingRequest struct {
	SchedulingDisabled *bool `json:"schedulingDisabled"`
}

func (s *Service) setHostScheduling(w http.ResponseWriter, r *http.Request) {
	var input hostSchedulingRequest
	if !decode(w, r, &input) {
		return
	}
	if input.SchedulingDisabled == nil {
		problem(w, http.StatusBadRequest, errors.New("schedulingDisabled must be a boolean"))
		return
	}
	setter, ok := s.Store.(store.HostSchedulingSetter)
	if !ok {
		problem(w, http.StatusInternalServerError, errors.New("atomic host scheduling updates are unavailable"))
		return
	}
	host, err := setter.SetHostSchedulingDisabled(r.Context(), r.PathValue("id"), *input.SchedulingDisabled)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			problem(w, http.StatusNotFound, err)
		case errors.Is(err, store.ErrHostArchived):
			problem(w, http.StatusConflict, err)
		default:
			problem(w, http.StatusInternalServerError, err)
		}
		return
	}
	action := "enable_host_scheduling"
	if host.SchedulingDisabled {
		action = "disable_host_scheduling"
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: actor(r.Context()), Action: action,
		ResourceType: "host", ResourceID: host.ID, Result: "ok",
	})
	writeJSON(w, http.StatusOK, host)
}
