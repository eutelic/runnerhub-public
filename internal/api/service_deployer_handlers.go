package api

import (
	"errors"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/deployer"
	"net/http"
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
	result, err := s.DeployerBootstrap.Install(r.Context(), in)
	if err != nil {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "install_deployer", ResourceType: "deployment", ResourceID: "runnerhub-deployer", Result: "error", SafeDetail: truncate(err.Error())})
		problem(w, 502, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "install_deployer", ResourceType: "deployment", ResourceID: result.DeployerApp, Result: "ok", SafeDetail: result.DeployerURL})
	writeJSON(w, 201, result)
}
