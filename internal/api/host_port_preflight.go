package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

const defaultRedeployerPort = 8790

type hostPortPreflightRequest struct {
	Port int `json:"port"`
}

type hostPortPreflightResponse struct {
	HostID string    `json:"hostId"`
	Port   int       `json:"port"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

func (s *Service) hostPortPreflight(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("TrueNAS host-port checks require admin access"))
		return
	}
	var in hostPortPreflightRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Port < 1024 || in.Port > 65535 {
		problem(w, http.StatusBadRequest, errors.New("redeployer port must be between 1024 and 65535"))
		return
	}
	result, err := s.probeTrueNASHostPort(r.Context(), r.PathValue("id"), in.Port)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, hostPortPreflightResponse{HostID: r.PathValue("id"), Port: in.Port, Status: result.Status, At: time.Now().UTC()})
}

func (s *Service) probeTrueNASHostPort(ctx context.Context, hostID string, port int) (agent.HostPortProbeResult, error) {
	if port < 1024 || port > 65535 {
		return agent.HostPortProbeResult{}, errors.New("redeployer port must be between 1024 and 65535")
	}
	if s.Hub == nil {
		return agent.HostPortProbeResult{}, errors.New("authenticated Docker host command channel is unavailable")
	}
	host, err := s.Store.GetHost(ctx, strings.TrimSpace(hostID))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return agent.HostPortProbeResult{}, errors.New("selected TrueNAS host is not enrolled")
		}
		return agent.HostPortProbeResult{}, errors.New("selected TrueNAS host could not be inspected")
	}
	if host.Provider != "docker" || host.State != core.HostOnline {
		return agent.HostPortProbeResult{}, errors.New("selected TrueNAS host must be an online Docker host")
	}
	commandID := "port-probe-" + randomID()
	waiter := make(chan agent.CommandResult, 1)
	s.hostPortProbeMu.Lock()
	if s.hostPortProbeWaiters == nil {
		s.hostPortProbeWaiters = map[string]chan agent.CommandResult{}
	}
	s.hostPortProbeWaiters[commandID] = waiter
	s.hostPortProbeMu.Unlock()
	defer func() {
		s.hostPortProbeMu.Lock()
		delete(s.hostPortProbeWaiters, commandID)
		s.hostPortProbeMu.Unlock()
	}()
	command := agent.Command{
		ID: commandID, WorkerID: commandID, Provider: "docker", Type: agent.CommandHostPortProbe,
		HostPortProbe: &agent.HostPortProbeSpec{HostID: host.ID, Port: port},
	}
	if err := s.Hub.Enqueue(host.ID, command); err != nil {
		return agent.HostPortProbeResult{}, errors.New("could not queue the authenticated TrueNAS host-port check")
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case result := <-waiter:
		if !result.OK || result.HostPortProbe == nil || result.HostPortProbe.Port != port {
			return agent.HostPortProbeResult{}, errors.New("TrueNAS host-port check did not return a verifiable result")
		}
		if result.HostPortProbe.Status != "available" && result.HostPortProbe.Status != "occupied" {
			return agent.HostPortProbeResult{}, errors.New("TrueNAS host-port check returned an unknown status")
		}
		return *result.HostPortProbe, nil
	case <-ctx.Done():
		_ = s.Store.AckCommand(context.Background(), commandID, time.Now().UTC())
		return agent.HostPortProbeResult{}, errors.New("TrueNAS host-port check was cancelled before a result arrived")
	case <-timer.C:
		_ = s.Store.AckCommand(context.Background(), commandID, time.Now().UTC())
		return agent.HostPortProbeResult{}, errors.New("no response from selected TrueNAS Docker host within 15 seconds")
	}
}

func (s *Service) handleHostPortProbeResult(ctx context.Context, rec store.CommandRecord, hostID string, result agent.CommandResult) error {
	probe := rec.Command.HostPortProbe
	if rec.Command.Type != agent.CommandHostPortProbe || probe == nil || rec.HostID != hostID || probe.HostID != hostID ||
		rec.Command.WorkerID != result.WorkerID || result.HostPortProbe != nil && result.HostPortProbe.Port != probe.Port {
		return errors.New("host-port probe result identity mismatch")
	}
	s.hostPortProbeMu.Lock()
	waiter := s.hostPortProbeWaiters[rec.Command.ID]
	s.hostPortProbeMu.Unlock()
	if waiter != nil {
		select {
		case waiter <- result:
		default:
		}
	}
	return nil
}
