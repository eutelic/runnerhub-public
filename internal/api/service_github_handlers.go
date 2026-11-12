package api

import (
	"errors"
	"github.com/eutelic/runnerhub/internal/githubauth"
	"net/http"
	"strings"
)

func (s *Service) githubAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		writeJSON(w, 200, map[string]bool{"enabled": false})
		return
	}
	writeJSON(w, 200, s.GitHubAuth.Status(r))
}
func (s *Service) githubLogin(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	s.GitHubAuth.BeginLogin(w, r)
}
func (s *Service) githubCallback(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	s.GitHubAuth.Callback(w, r)
}
func (s *Service) githubInstall(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	s.GitHubAuth.Install(w, r)
}
func (s *Service) githubSetup(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	s.GitHubAuth.Setup(w, r)
}
func (s *Service) githubLogout(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	s.GitHubAuth.Logout(w, r)
}
func (s *Service) githubInstallations(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	installations, err := s.GitHubAuth.AccessibleInstallations(r)
	if err != nil {
		problem(w, 401, err)
		return
	}
	writeJSON(w, 200, installations)
}
func (s *Service) queuedGitHubJobs(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	owner, repo := strings.TrimSpace(r.PathValue("owner")), strings.TrimSpace(r.PathValue("repo"))
	if owner == "" || repo == "" {
		problem(w, 400, errors.New("repository owner and name are required"))
		return
	}
	report, err := s.GitHubAuth.QueuedJobs(r, owner+"/"+repo)
	if err != nil {
		var queueError *githubauth.QueueInspectionError
		if errors.As(err, &queueError) {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Bad Gateway", "detail": truncate(err.Error()),
				"code": queueError.Code, "requestId": queueError.RequestID,
				"acceptedPermissions": queueError.AcceptedPermissions,
				"retryAt":             queueError.RetryAt,
			})
			return
		}
		problem(w, 502, err)
		return
	}
	writeJSON(w, 200, report)
}
func (s *Service) connectedRepositories(w http.ResponseWriter, r *http.Request) {
	if s.GitHubAuth == nil {
		problem(w, 503, errors.New("github browser authentication is not configured"))
		return
	}
	repos, err := s.GitHubAuth.ConnectedRepositories(r)
	if err != nil {
		problem(w, 401, err)
		return
	}
	writeJSON(w, 200, repos)
}
