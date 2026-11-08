package cireconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

const (
	workflowRunQuarantineFirstDelay = 6 * time.Hour
	workflowRunQuarantineMaxDelay   = 7 * 24 * time.Hour
)

func permanentWorkflowRunMetadataFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, githubrunner.ErrNotFound) {
		return "run_not_found"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "rate limit") ||
		strings.Contains(message, "secondary rate limit") ||
		strings.Contains(message, "returned 429") {
		return ""
	}
	if strings.Contains(message, "returned 403") &&
		(strings.Contains(message, "resource not accessible by integration") ||
			strings.Contains(message, "permission") ||
			strings.Contains(message, "not authorized")) {
		return "permission_denied"
	}
	return ""
}

func workflowRunQuarantineDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := workflowRunQuarantineFirstDelay
	for n := 1; n < failures && delay < workflowRunQuarantineMaxDelay; n++ {
		delay *= 2
	}
	if delay > workflowRunQuarantineMaxDelay {
		delay = workflowRunQuarantineMaxDelay
	}
	return delay
}

func (r *Reconciler) quarantineWorkflowRun(
	ctx context.Context,
	metadataStore store.CIMetadataStore,
	repository string,
	runID int64,
	failure error,
	now time.Time,
) (bool, error) {
	reason := permanentWorkflowRunMetadataFailure(failure)
	if reason == "" {
		return false, nil
	}
	record, err := metadataStore.GetCIWorkflowRunQuarantine(ctx, repository, runID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if record.FailureCount < 20 {
		record.FailureCount++
	}
	record.Repository = repository
	record.RunID = runID
	record.Reason = reason
	record.LastFailureAt = now
	record.RetryAfter = now.Add(workflowRunQuarantineDelay(record.FailureCount))
	if err := metadataStore.UpsertCIWorkflowRunQuarantine(ctx, record); err != nil {
		return false, err
	}
	if r.Store != nil {
		_ = r.Store.AppendAudit(ctx, core.AuditEvent{
			Actor:        "ci-reconciler",
			Action:       "quarantine_workflow_run",
			ResourceType: "ci_workflow_run",
			ResourceID:   fmt.Sprintf("%s#%d", repository, runID),
			Result:       "quarantined",
			SafeDetail:   fmt.Sprintf("%s; recheck after %s", reason, record.RetryAfter.UTC().Format(time.RFC3339)),
		})
	}
	return true, nil
}
