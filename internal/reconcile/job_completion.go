package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

// Only poll old jobs: the ordinary scale-set completion callback or provider
// exit should own the fast path. A single delayed CI job is not automatically
// presumed broken.
const (
	githubJobCompletionMinAge       = 30 * time.Minute
	githubJobCompletionPollInterval = 15 * time.Minute
	githubJobCompletionSettle       = 2 * time.Minute
)

func (r *Reconciler) githubJobCompletionPollDue(w core.Worker, now time.Time) bool {
	if w.State != core.WorkerBusy || w.WorkflowRunID <= 0 ||
		w.Scope.Kind != core.ScopeRepository || strings.TrimSpace(w.RunnerName) == "" ||
		w.StartedAt.IsZero() || now.Sub(w.StartedAt) < githubJobCompletionMinAge {
		return false
	}
	if w.WorkflowRunAttempt > 0 {
		if _, ok := r.GitHub.(githubrunner.CIMetadataBackend); !ok {
			return false
		}
	} else {
		if _, ok := r.GitHub.(githubrunner.WorkflowJobsBackend); !ok {
			return false
		}
	}
	key := "job:" + w.ID
	r.githubPollMu.Lock()
	defer r.githubPollMu.Unlock()
	if r.githubLastPoll == nil {
		r.githubLastPoll = map[string]time.Time{}
	}
	if last, ok := r.githubLastPoll[key]; ok && now.Sub(last) < githubJobCompletionPollInterval {
		return false
	}
	r.githubLastPoll[key] = now
	return true
}

// tryReconcileCompletedGitHubJob releases the *pool concurrency* reservation
// only when GitHub reports a unique, completed job for the exact ephemeral
// runner. Provider resource reservation continues until host-side cleanup.
// The normal reconciler then queues deletion, preserving audit/history.
func (r *Reconciler) tryReconcileCompletedGitHubJob(ctx context.Context, w *core.Worker, now time.Time) error {
	if w == nil || r.GitHub == nil {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	jobs, err := githubrunner.AssignedWorkflowJobs(queryCtx, r.GitHub, *w)
	if err != nil {
		return fmt.Errorf("GitHub completed-job verification unavailable for worker %s: %w", w.ID, err)
	}
	job, found := githubrunner.MatchAssignedWorkflowJob(w.RunnerName, w.JobName, w.JobID, jobs)
	if !found || !strings.EqualFold(job.Status, "completed") ||
		!githubrunner.IsTerminalJobConclusion(job.Conclusion) {
		return nil
	}
	// A nonzero GitHub completion timestamp avoids confusing an intermediate
	// in_progress/completed transition with durable terminal evidence.
	if job.CompletedAt.IsZero() || job.CompletedAt.After(now.Add(-githubJobCompletionSettle)) {
		return nil
	}

	latest, err := r.Store.GetWorker(ctx, w.ID)
	if err != nil {
		return err
	}
	if latest.State != core.WorkerBusy ||
		latest.RunnerName != w.RunnerName || latest.WorkflowRunID != w.WorkflowRunID ||
		latest.JobID != w.JobID || latest.JobName != w.JobName {
		// An agent or scale-set event may have finished it while GitHub was queried.
		return nil
	}
	outcome := strings.ToLower(strings.TrimSpace(job.Conclusion))
	latest.JobResult = outcome
	latest.CompletedAt = job.CompletedAt.UTC()
	if !latest.StartedAt.IsZero() && latest.CompletedAt.Before(latest.StartedAt) {
		latest.CompletedAt = now.UTC()
	}
	if outcome == "success" {
		latest.State = core.WorkerCompleted
	} else {
		latest.State = core.WorkerFailed
		latest.FailedAt = now.UTC()
		latest.FailureReason = "GitHub confirmed terminal job conclusion: " + outcome
	}
	latest.UpdatedAt = now.UTC()
	if err := r.Store.UpsertWorker(ctx, latest); err != nil {
		return err
	}
	*w = latest
	r.clearGitHubPoll(latest.ID)
	_ = r.Store.AppendAudit(ctx, core.AuditEvent{
		Actor: "reconciler", Action: "reconcile_github_terminal",
		ResourceType: "worker", ResourceID: latest.ID, Result: "ok",
		SafeDetail: fmt.Sprintf("GitHub job %d concluded %s; exact runner assignment verified", job.ID, outcome),
		CreatedAt:  now.UTC(),
	})
	return nil
}
