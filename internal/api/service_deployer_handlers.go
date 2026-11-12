package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/deployer"
)

func (s *Service) deployerStatus(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, 403, errors.New("deployer configuration requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		writeJSON(w, 200, deployer.BootstrapStatus{Available: false, Detail: "deployer bootstrap is not configured"})
		return
	}
	writeJSON(w, 200, s.DeployerBootstrap.Status(r.Context()))
}
func (s *Service) installDeployer(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, 403, errors.New("deployer installation requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		problem(w, 503, errors.New("deployer bootstrap is not configured"))
		return
	}
	var in deployer.BootstrapRequest
	if !decode(w, r, &in) {
		return
	}
	port := in.Port
	if port == 0 {
		port = defaultRedeployerPort
	}
	if strings.TrimSpace(in.PortCheckHostID) == "" {
		problem(w, http.StatusServiceUnavailable, errors.New("select the enrolled Docker host that matches this TrueNAS URL; install is blocked until host-side port preflight is available"))
		return
	}
	portResult, err := s.probeTrueNASHostPort(r.Context(), in.PortCheckHostID, port)
	if err != nil {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "preflight_deployer_port", ResourceType: "host", ResourceID: in.PortCheckHostID, Result: "unknown", SafeDetail: fmt.Sprintf("port %d: %s", port, truncate(err.Error()))})
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	if portResult.Status != "available" {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "preflight_deployer_port", ResourceType: "host", ResourceID: in.PortCheckHostID, Result: "occupied", SafeDetail: fmt.Sprintf("port %d is occupied", port)})
		problem(w, http.StatusConflict, fmt.Errorf("redeployer port %d is already occupied on selected TrueNAS host; choose a different port", port))
		return
	}
	result, err := s.DeployerBootstrap.Install(r.Context(), in)
	if err != nil {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "install_deployer", ResourceType: "deployment", ResourceID: "runnerhub-deployer", Result: "error", SafeDetail: truncate(err.Error())})
		problem(w, 502, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "install_deployer", ResourceType: "deployment", ResourceID: result.DeployerApp, Result: "ok", SafeDetail: result.DeployerURL})
	writeJSON(w, 201, result)
}
