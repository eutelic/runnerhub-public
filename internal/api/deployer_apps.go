package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/deployer"
)

func (s *Service) deployerApps(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("deployer configuration requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("deployer is not configured"))
		return
	}
	apps, err := s.DeployerBootstrap.ManagedApps(r.Context())
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	rules, err := s.DeployerBootstrap.AutoPublishRules()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	byApp := make(map[string]deployer.AutoPublishRule, len(rules))
	for _, rule := range rules {
		byApp[rule.App] = rule
	}
	type appWithPublishing struct {
		deployer.ManagedApp
		Automatic *deployer.AutoPublishRule `json:"automatic,omitempty"`
	}
	response := make([]appWithPublishing, 0, len(apps))
	for _, app := range apps {
		item := appWithPublishing{ManagedApp: app}
		if rule, ok := byApp[app.Name]; ok {
			item.Automatic = &rule
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": response})
}

func (s *Service) setDeployerApp(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("deployer configuration requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("deployer is not configured"))
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		problem(w, http.StatusBadRequest, errors.New("enabled boolean is required"))
		return
	}
	app := r.PathValue("app")
	err := s.DeployerBootstrap.SetManagedApp(r.Context(), app, *input.Enabled)
	if err == nil && !*input.Enabled {
		err = s.DeployerBootstrap.DisableAutoPublish(app)
	}
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	detail, _ := json.Marshal(map[string]bool{"enabled": *input.Enabled})
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: actor(r.Context()), Action: "configure_truenas_redeploy",
		ResourceType: "deployment", ResourceID: app, Result: "ok", SafeDetail: string(detail),
	})
	writeJSON(w, http.StatusOK, map[string]any{"name": app, "enabled": *input.Enabled})
}

func (s *Service) redeployApp(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("deployer configuration requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("deployer is not configured"))
		return
	}
	app := r.PathValue("app")
	if err := s.DeployerBootstrap.RedeployManagedApp(r.Context(), app); err != nil {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
			Actor: actor(r.Context()), Action: "redeploy_truenas_app",
			ResourceType: "deployment", ResourceID: app, Result: "error", SafeDetail: truncate(err.Error()),
		})
		problem(w, http.StatusBadGateway, err)
		return
	}
	// A successful operator-initiated redeployment is the recovery step for a
	// failed or interrupted auto-publish. Only release the hold after success.
	if err := s.DeployerBootstrap.MarkManualAutoPublishRecovery(r.Context(), app); err != nil {
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
			Actor: actor(r.Context()), Action: "redeploy_truenas_app",
			ResourceType: "deployment", ResourceID: app, Result: "error",
			SafeDetail: truncate("TrueNAS redeployed; automatic publishing recovery verification failed: " + err.Error()),
		})
		problem(w, http.StatusInternalServerError, errors.New("TrueNAS redeployed but automatic publishing recovery could not be verified; inspect the app image and publishing status before resuming"))
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: actor(r.Context()), Action: "redeploy_truenas_app",
		ResourceType: "deployment", ResourceID: app, Result: "ok",
	})
	writeJSON(w, http.StatusOK, map[string]string{"name": app, "state": "deployed"})
}
