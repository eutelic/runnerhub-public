package githubrunner

import (
	"context"
	"errors"

	"github.com/eutelic/runnerhub/internal/core"
)

// AssignedWorkflowJobs reads the exact workflow attempt when the worker
// captured one. Falling back to "latest" for older attempts could falsely
// associate a reused runner name with a rerun.
func AssignedWorkflowJobs(ctx context.Context, backend Backend, w core.Worker) ([]WorkflowJob, error) {
	if w.WorkflowRunID <= 0 || w.Scope.Kind != core.ScopeRepository {
		return nil, errors.New("repository workflow run identity required")
	}
	if w.WorkflowRunAttempt > 0 {
		ci, ok := backend.(CIMetadataBackend)
		if !ok {
			return nil, errors.New("attempt-specific GitHub job verification is unavailable")
		}
		return ci.ListWorkflowJobsAttempt(ctx, w.Scope, w.WorkflowRunID, w.WorkflowRunAttempt)
	}
	b, ok := backend.(WorkflowJobsBackend)
	if !ok {
		return nil, errors.New("GitHub job verification is unavailable")
	}
	return b.ListWorkflowJobs(ctx, w.Scope, w.WorkflowRunID)
}
