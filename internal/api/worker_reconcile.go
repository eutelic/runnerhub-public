package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

type reconcileCompletedWorkerRequest struct {
	GitHubJobURL string `json:"githubJobUrl,omitempty"`
}

func parseVerifiedGitHubJobURL(raw string, scope core.GitHubScope) (int64, int64, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return 0, 0, errors.New("invalid GitHub job URL")
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0, 0, errors.New("GitHub job URL must use the canonical https://github.com host")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 7 || !strings.EqualFold(parts[0], scope.Owner) ||
		!strings.EqualFold(parts[1], scope.Repository) ||
		parts[2] != "actions" || parts[3] != "runs" || parts[5] != "job" {
		return 0, 0, errors.New("GitHub job URL does not match the worker's repository and job route")
	}
	runID, runErr := strconv.ParseInt(parts[4], 10, 64)
	jobID, jobErr := strconv.ParseInt(parts[6], 10, 64)
	if runErr != nil || jobErr != nil || runID <= 0 || jobID <= 0 {
		return 0, 0, errors.New("GitHub job URL must contain positive numeric run and job IDs")
	}
	return runID, jobID, nil
}

// A stuck local worker may keep a pool at its concurrency limit even after
// GitHub has conclusively finished its job. Recovery is deliberately an
// operator action: no state mutation happens during ordinary progress reads.
func (s *Service) reconcileGitHubCompletedWorker(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("worker reconciliation requires admin access"))
		return
	}
	if s.Hub == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("agent cleanup service is unavailable"))
		return
	}
	if _, ok := s.GitHub.(githubrunner.WorkflowJobsBackend); !ok {
		problem(w, http.StatusServiceUnavailable, errors.New("GitHub job verification is unavailable"))
		return
	}
	var input reconcileCompletedWorkerRequest
	if r.ContentLength != 0 {
		if !decode(w, r, &input) {
			return
		}
	}
	id := strings.TrimSpace(r.PathValue("id"))
	wk, err := s.Store.GetWorker(r.Context(), id)
	if err != nil {
		problem(w, http.StatusNotFound, errors.New("worker not found"))
		return
	}
	if wk.State != core.WorkerBusy || wk.Scope.Kind != core.ScopeRepository || wk.RunnerName == "" {
		problem(w, http.StatusConflict, errors.New("worker must be busy with an identifiable GitHub runner"))
		return
	}
	runID := wk.WorkflowRunID
	var suppliedJobID int64
	if strings.TrimSpace(input.GitHubJobURL) != "" {
		parsedRunID, parsedJobID, parseErr := parseVerifiedGitHubJobURL(input.GitHubJobURL, wk.Scope)
		if parseErr != nil {
			problem(w, http.StatusBadRequest, parseErr)
			return
		}
		if runID > 0 && parsedRunID != runID {
			problem(w, http.StatusConflict, errors.New("supplied GitHub run differs from worker's recorded run"))
			return
		}
		if wk.JobID != "" && wk.JobID != strconv.FormatInt(parsedJobID, 10) {
			problem(w, http.StatusConflict, errors.New("supplied GitHub job differs from worker's recorded job"))
			return
		}
		runID = parsedRunID
		suppliedJobID = parsedJobID
	}
	if runID <= 0 {
		problem(w, http.StatusConflict, errors.New("worker has no GitHub run ID; paste its completed GitHub job URL to verify"))
		return
	}
	verifiedWorker := wk
	verifiedWorker.WorkflowRunID = runID
	if suppliedJobID > 0 {
		verifiedWorker.JobID = strconv.FormatInt(suppliedJobID, 10)
	}
	var jobs []githubrunner.WorkflowJob
	if wk.WorkflowRunAttempt > 0 {
		queryCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		jobs, err = githubrunner.AssignedWorkflowJobs(queryCtx, s.GitHub, verifiedWorker)
	} else {
		jobs, err = s.cachedWorkflowJobs(r.Context(), verifiedWorker.Scope, runID)
	}
	if err != nil {
		problem(w, http.StatusBadGateway, errors.New("could not verify GitHub job state; no changes made"))
		return
	}
	job, ok := matchGitHubJobForReconciliation(verifiedWorker, jobs)
	if !ok || !strings.EqualFold(job.Status, "completed") || !githubrunner.IsTerminalJobConclusion(job.Conclusion) {
		problem(w, http.StatusConflict, errors.New("no uniquely matching completed GitHub job; no changes made"))
		return
	}
	// Check again immediately before mutation; another worker event may have
	// completed the record while GitHub was being queried.
	latest, err := s.Store.GetWorker(r.Context(), id)
	if err != nil || latest.State != core.WorkerBusy ||
		latest.RunnerName != wk.RunnerName || latest.WorkflowRunID != wk.WorkflowRunID ||
		latest.WorkflowRunAttempt != wk.WorkflowRunAttempt ||
		latest.JobID != wk.JobID || latest.JobName != wk.JobName {
		problem(w, http.StatusConflict, errors.New("worker changed while verifying GitHub; refresh and try again"))
		return
	}
	now := time.Now().UTC()
	latest.WorkflowRunID = runID
	if suppliedJobID > 0 && latest.JobID == "" {
		latest.JobID = strconv.FormatInt(suppliedJobID, 10)
	}
	latest.JobResult = job.Conclusion
	if strings.EqualFold(job.Conclusion, "success") {
		latest.State = core.WorkerCompleted
	} else {
		latest.State = core.WorkerFailed
		latest.FailureReason = "GitHub job completed with " + job.Conclusion + "; state reconciled by administrator"
		latest.FailedAt = now
	}
	latest.CompletedAt = normalizeCompletionTime(latest.StartedAt, job.CompletedAt, now)
	if latest.CompletedAt.IsZero() {
		latest.CompletedAt = now
	}
	latest.UpdatedAt = now
	if err := s.Store.UpsertWorker(r.Context(), latest); err != nil {
		problem(w, http.StatusInternalServerError, errors.New("failed to record reconciled worker"))
		return
	}
	err = s.enqueueTerminalCleanup(r.Context(), &latest)
	result := "ok"
	detail := fmt.Sprintf("GitHub job %d concluded %s; worker verified and reconciled", job.ID, job.Conclusion)
	if err != nil {
		result = "cleanup_error"
		detail = "GitHub completion verified; worker marked terminal but cleanup command could not be queued"
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
		Actor: actor(r.Context()), Action: "reconcile_github_worker", ResourceType: "worker",
		ResourceID: latest.ID, Result: result, SafeDetail: detail,
	})
	if err != nil {
		// State is already terminal and the concurrency reservation is released.
		// Return a partial outcome rather than claiming cleanup was successful.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"worker": latest, "cleanupQueued": false,
			"detail": "GitHub completion confirmed and worker state reconciled, but agent cleanup needs attention.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"worker": latest, "cleanupQueued": strings.TrimSpace(latest.ProviderHandle) != "",
		"detail": "GitHub completion confirmed and worker state reconciled. Agent cleanup queued if needed.",
	})
}

// Verify exact runner identity or GitHub job ID. A matching job title alone
// can be ambiguous within workflow matrices and must not release capacity.
func matchGitHubJobForReconciliation(wk core.Worker, jobs []githubrunner.WorkflowJob) (githubrunner.WorkflowJob, bool) {
	return githubrunner.MatchAssignedWorkflowJob(wk.RunnerName, wk.JobName, wk.JobID, jobs)
}
