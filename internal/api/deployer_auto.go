package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/deployer"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

func (s *Service) configureAutoPublishing(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("automatic deployment configuration requires admin access"))
		return
	}
	if s.DeployerBootstrap == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("deployer is not configured"))
		return
	}
	app := r.PathValue("app")
	var input struct {
		Enabled    *bool  `json:"enabled"`
		Repository string `json:"repository"`
		Workflow   string `json:"workflow"`
		Image      string `json:"image"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		problem(w, http.StatusBadRequest, errors.New("enabled boolean is required"))
		return
	}
	if !*input.Enabled {
		if err := s.DeployerBootstrap.DisableAutoPublish(app); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
			Actor: actor(r.Context()), Action: "disable_auto_publish",
			ResourceType: "deployment", ResourceID: app, Result: "ok",
		})
		writeJSON(w, http.StatusOK, map[string]any{"app": app, "enabled": false})
		return
	}
	apps, err := s.DeployerBootstrap.ManagedApps(r.Context())
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	allowed := false
	image := strings.TrimSpace(input.Image)
	for _, candidate := range apps {
		if candidate.Name == app && candidate.Enabled && !candidate.Protected && candidate.CustomApp {
			for _, candidateImage := range candidate.Images {
				if strings.EqualFold(candidateImage, image) {
					allowed = true
					break
				}
			}
			break
		}
	}
	if !allowed {
		problem(w, http.StatusForbidden, errors.New("automatic digest-pinned redeployment requires an enabled, unprotected custom TrueNAS Compose app with the selected GHCR image"))
		return
	}
	repository := strings.TrimSpace(input.Repository)
	if !deployer.ImageMatchesPublicationRepository(image, repository) {
		problem(w, http.StatusBadRequest, errors.New("automatic redeploy requires a matching ghcr.io/<repository>:latest image installed in this TrueNAS app"))
		return
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		problem(w, http.StatusBadRequest, errors.New("GitHub repository must be owner/name"))
		return
	}
	if s.Catalog == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("GitHub App repository lookup unavailable"))
		return
	}
	repo, err := s.Catalog.GetRepository(r.Context(), parts[0], parts[1])
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	if !repo.Private || !strings.EqualFold(repo.FullName, repository) || repo.DefaultBranch != "main" {
		problem(w, http.StatusForbidden, errors.New("automatic deployment requires a connected private GitHub repository with default branch main"))
		return
	}
	workflow := strings.TrimSpace(input.Workflow)
	if err := githubrunner.ValidatePublicationWorkflow(workflow); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	backend, ok := s.GitHub.(githubrunner.PublicationRunsBackend)
	if !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("GitHub workflow status lookup unavailable"))
		return
	}
	scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: parts[0], Repository: parts[1]}
	latest, found, err := backend.LatestPublicationRun(r.Context(), scope, workflow, "main")
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	rule := deployer.AutoPublishRule{
		App: app, Repository: repo.FullName, Workflow: workflow, Image: image, Branch: "main",
	}
	rule.LastRunID, rule.LastAttempt = initialPublicationCursor(latest, found)
	if err := s.DeployerBootstrap.SetAutoPublish(rule); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: actor(r.Context()), Action: "enable_auto_publish",
		ResourceType: "deployment", ResourceID: app, Result: "ok",
		SafeDetail: rule.Repository + " " + rule.Workflow + " " + rule.Image,
	})
	writeJSON(w, http.StatusOK, map[string]any{"app": app, "enabled": true, "automatic": rule})
}

// initialPublicationCursor prevents completed historical releases from replaying
// at opt-in, but deliberately leaves an in-flight publishing run eligible when
// it completes. Marking an in-progress run as handled would silently miss its
// first successfully published image.
func initialPublicationCursor(latest githubrunner.WorkflowRunMetadata, found bool) (int64, int) {
	if !found || !strings.EqualFold(latest.Status, "completed") {
		return 0, 0
	}
	return latest.ID, latest.Attempt
}
