package githubrunner

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
)

var publisherWorkflowName = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\\.ya?ml$")

// PublicationRunsBackend reads Actions runs using the existing GitHub App's
// Actions-read permission. No new webhook endpoint or workflow secret is needed.
type PublicationRunsBackend interface {
	LatestPublicationRun(context.Context, core.GitHubScope, string, string) (WorkflowRunMetadata, bool, error)
}

func ValidatePublicationWorkflow(name string) error {
	if !publisherWorkflowName.MatchString(name) || strings.Contains(name, "..") {
		return errors.New("publication workflow must be a .github/workflows/*.yml or *.yaml filename")
	}
	return nil
}

// LatestPublicationRun returns the latest run on the exact configured branch,
// including running/failed runs, so a failed newer run never falls back to an
// earlier successful publication.
func (b *HTTPBackend) LatestPublicationRun(ctx context.Context, scope core.GitHubScope, workflow, branch string) (WorkflowRunMetadata, bool, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return WorkflowRunMetadata{}, false, err
	}
	if err := ValidatePublicationWorkflow(workflow); err != nil {
		return WorkflowRunMetadata{}, false, err
	}
	if strings.TrimSpace(branch) != "main" {
		return WorkflowRunMetadata{}, false, errors.New("automatic publication requires the main branch")
	}
	var payload struct {
		WorkflowRuns []workflowRunPayload `json:"workflow_runs"`
	}
	path := b.scopeBasePath(scope) + "/actions/workflows/" + url.PathEscape(workflow) + "/runs?per_page=1&branch=main"
	if err := b.doJSON(ctx, scope, http.MethodGet, path, nil, http.StatusOK, &payload); err != nil {
		return WorkflowRunMetadata{}, false, err
	}
	if len(payload.WorkflowRuns) == 0 {
		return WorkflowRunMetadata{}, false, nil
	}
	return workflowRunMetadata(payload.WorkflowRuns[0]), true, nil
}
