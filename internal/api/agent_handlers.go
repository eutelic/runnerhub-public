package api

import (
	"errors"
	"net/http"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/store"
)

// All agent endpoints use the same unauthorized response without exposing
// whether a worker ID or host credential exists.
func unauthorizedAgent(w http.ResponseWriter) {
	problem(w, http.StatusUnauthorized, errors.New("unauthorized agent"))
}

func (s *Service) enroll(w http.ResponseWriter, r *http.Request) {
	var in agent.EnrollRequest
	if !decode(w, r, &in) {
		return
	}
	res, err := s.Hub.Enroll(r.Context(), bearer(r), in)
	if err != nil {
		problem(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Service) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in agent.Heartbeat
	if !decode(w, r, &in) {
		return
	}
	if !s.Hub.Authenticate(in.HostID, bearer(r)) {
		unauthorizedAgent(w)
		return
	}
	if err := s.Hub.Heartbeat(r.Context(), in); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("X-RunnerHub-Agent-Token", s.Hub.RefreshToken(in.HostID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) commands(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host_id")
	if !s.Hub.Authenticate(hostID, bearer(r)) {
		unauthorizedAgent(w)
		return
	}
	writeJSON(w, http.StatusOK, s.Hub.Next(hostID))
}

func (s *Service) results(w http.ResponseWriter, r *http.Request) {
	var in agent.CommandResult
	if !decode(w, r, &in) {
		return
	}

	// Worker results are authenticated against the worker's host. Update
	// commands have an operation ID instead of a worker ID, so resolve their
	// host from the exact persisted command before checking the bearer token.
	hostID := ""
	if command, err := s.Store.GetCommand(r.Context(), in.CommandID); err == nil &&
		(command.Command.Type == agent.CommandUpdateAgent || command.Command.Type == agent.CommandHostPortProbe) {
		if command.Command.WorkerID != in.WorkerID {
			unauthorizedAgent(w)
			return
		}
		hostID = command.HostID
	} else {
		worker, err := s.Store.GetWorker(r.Context(), in.WorkerID)
		if errors.Is(err, store.ErrNotFound) {
			unauthorizedAgent(w)
			return
		}
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		hostID = worker.HostID
	}
	if hostID == "" || s.Hub == nil || !s.Hub.Authenticate(hostID, bearer(r)) {
		unauthorizedAgent(w)
		return
	}
	if err := s.Hub.HandleResult(r.Context(), hostID, in); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
