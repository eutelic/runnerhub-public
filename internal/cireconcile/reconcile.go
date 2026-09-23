package cireconcile

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type Reconciler struct {
	Store    store.Store
	GitHub   githubrunner.CIMetadataBackend
	Interval time.Duration
	Lookback time.Duration
	Now      func() time.Time
}

func (r *Reconciler) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Once(ctx); err != nil {
			if r.Store != nil {
				_ = r.Store.AppendAudit(ctx, core.AuditEvent{
					Actor: "ci-reconciler", Action: "reconcile", ResourceType: "ci",
					Result: "error", SafeDetail: truncate(err.Error()),
				})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Reconciler) Once(ctx context.Context) error {
	if r.Store == nil || r.GitHub == nil {
		return nil
	}
	metadataStore, metadataOK := r.Store.(store.CIMetadataStore)
	findingStore, findingsOK := r.Store.(store.CIFindingStore)
	certificateStore, certificatesOK := r.Store.(store.CIValidationCertificateStore)
	if !metadataOK || !findingsOK {
		return errors.New("CI reconciliation requires metadata and finding persistence")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	lookback := r.Lookback
	if lookback <= 0 {
		lookback = 72 * time.Hour
	}
	cutoff := now.Add(-lookback)

	workers, err := r.Store.ListWorkers(ctx)
	if err != nil {
		return err
	}
	candidates := make([]core.Worker, 0, len(workers))
	for _, worker := range workers {
		if strings.TrimSpace(worker.Repository) == "" || worker.WorkflowRunID <= 0 {
			continue
		}
		if worker.StartedAt.IsZero() {
			continue
		}
		terminalAt := worker.CompletedAt
		if terminalAt.IsZero() {
			terminalAt = worker.FailedAt
		}
		if terminalAt.IsZero() || !worker.StartedAt.Before(terminalAt) {
			continue
		}
		if !terminalAt.After(cutoff) || worker.StartedAt.After(now) {
			continue
		}
		candidates = append(candidates, worker)
	}
	sortWorkersByCompletion(candidates)

	persistedRuns := map[string]store.CIWorkflowRun{}
	repositories := map[string]struct{}{}
	for _, worker := range candidates {
		repository := normalizeRepository(worker.Repository)
		if repository != "" {
			repositories[repository] = struct{}{}
		}
	}
	for repository := range repositories {
		runs, listErr := metadataStore.ListCIWorkflowRuns(ctx, repository, cutoff, now)
		if listErr != nil {
			return listErr
		}
		for _, run := range runs {
			persistedRuns[runIdentityKey(run.Repository, run.RunID, run.Attempt)] = run
		}
	}

	type runKey struct {
		repository string
		runID      int64
	}
	latestRuns := map[runKey]githubrunner.WorkflowRunMetadata{}
	attemptRuns := map[string]store.CIWorkflowRun{}
	jobsByAttempt := map[string][]githubrunner.WorkflowJob{}
	prs := map[string]store.CIPullRequest{}
	var errs []error

	for i := range candidates {
		worker := &candidates[i]
		repository := normalizeRepository(worker.Repository)
		scope, ok := repositoryScope(repository)
		if !ok {
			errs = append(errs, fmt.Errorf("worker %s has invalid repository %q", worker.ID, worker.Repository))
			continue
		}
		attempt := worker.WorkflowRunAttempt
		attemptKey := ""
		storedRun := store.CIWorkflowRun{}
		if attempt > 0 {
			attemptKey = runIdentityKey(repository, worker.WorkflowRunID, attempt)
			storedRun = persistedRuns[attemptKey]
		}

		var matchedJob *githubrunner.WorkflowJob
		if storedRun.RunID == 0 || strings.TrimSpace(storedRun.HeadTreeSHA) == "" {
			key := runKey{repository: repository, runID: worker.WorkflowRunID}
			latest, ok := latestRuns[key]
			if !ok {
				latest, err = r.GitHub.GetWorkflowRun(ctx, scope, worker.WorkflowRunID)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s run %d: %w", repository, worker.WorkflowRunID, err))
					continue
				}
				latestRuns[key] = latest
			}
			if attempt <= 0 {
				attempt, matchedJob, err = r.findWorkerAttempt(ctx, scope, *worker, latest, jobsByAttempt)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s worker %s attempt: %w", repository, worker.ID, err))
					continue
				}
			}
			if attempt <= 0 {
				continue
			}
			attemptKey = runIdentityKey(repository, worker.WorkflowRunID, attempt)
			if cached := persistedRuns[attemptKey]; cached.RunID != 0 && strings.TrimSpace(cached.HeadTreeSHA) != "" {
				storedRun = cached
			} else {
				run := latest
				if latest.Attempt != attempt {
					run, err = r.GitHub.GetWorkflowRunAttempt(ctx, scope, worker.WorkflowRunID, attempt)
					if err != nil {
						errs = append(errs, fmt.Errorf("%s run %d attempt %d: %w", repository, worker.WorkflowRunID, attempt, err))
						continue
					}
				}
				storedRun = store.CIWorkflowRun{
					Repository: repository, RunID: run.ID, Attempt: attempt, Name: run.Name, Path: run.Path,
					Event: run.Event, Status: run.Status, Conclusion: run.Conclusion, HeadBranch: run.HeadBranch,
					HeadSHA: run.HeadSHA, HeadTreeSHA: run.HeadTreeSHA, CreatedAt: run.CreatedAt, RunStartedAt: run.RunStartedAt,
					UpdatedAt: run.UpdatedAt, PullRequestNumbers: append([]int(nil), run.PullRequestNumbers...),
				}
				if storedRun.RunID == 0 {
					storedRun.RunID = worker.WorkflowRunID
				}
				if storedRun.Attempt == 0 {
					storedRun.Attempt = attempt
				}
				if err := metadataStore.UpsertCIWorkflowRun(ctx, storedRun); err != nil {
					errs = append(errs, err)
					continue
				}
				persistedRuns[attemptKey] = storedRun
			}
		}
		attemptRuns[attemptKey] = storedRun

		changed := false
		if worker.WorkflowRunAttempt != attempt {
			worker.WorkflowRunAttempt = attempt
			changed = true
		}
		if matchedJob != nil {
			jobID := strconv.FormatInt(matchedJob.ID, 10)
			if strings.TrimSpace(worker.JobID) == "" {
				worker.JobID = jobID
				changed = true
			}
			if strings.TrimSpace(worker.JobName) == "" && strings.TrimSpace(matchedJob.Name) != "" {
				worker.JobName = matchedJob.Name
				changed = true
			}
			if strings.TrimSpace(worker.JobResult) == "" && strings.TrimSpace(matchedJob.Conclusion) != "" {
				worker.JobResult = matchedJob.Conclusion
				changed = true
			}
			if worker.StartedAt.IsZero() && !matchedJob.StartedAt.IsZero() {
				worker.StartedAt = matchedJob.StartedAt
				changed = true
			}
			if worker.CompletedAt.IsZero() && !matchedJob.CompletedAt.IsZero() {
				worker.CompletedAt = matchedJob.CompletedAt
				changed = true
			}
		}
		if changed {
			if err := r.Store.UpsertWorker(ctx, *worker); err != nil {
				errs = append(errs, err)
				continue
			}
		}

		for _, number := range storedRun.PullRequestNumbers {
			if number <= 0 {
				continue
			}
			prKey := fmt.Sprintf("%s#%d", repository, number)
			if _, ok := prs[prKey]; ok {
				continue
			}
			pr, getErr := metadataStore.GetCIPullRequest(ctx, repository, number)
			if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
				errs = append(errs, getErr)
				continue
			}
			if errors.Is(getErr, store.ErrNotFound) || pr.MergedAt.IsZero() {
				live, fetchErr := r.GitHub.GetPullRequest(ctx, scope, number)
				if fetchErr != nil {
					errs = append(errs, fmt.Errorf("%s PR #%d: %w", repository, number, fetchErr))
					continue
				}
				pr = store.CIPullRequest{
					Repository: repository, Number: live.Number, State: live.State, MergedAt: live.MergedAt,
					HeadRef: live.HeadRef, HeadSHA: live.HeadSHA, BaseRef: live.BaseRef,
				}
				if err := metadataStore.UpsertCIPullRequest(ctx, pr); err != nil {
					errs = append(errs, err)
					continue
				}
			}
			prs[prKey] = pr
		}
	}

	runByWorker := make(map[string]store.CIWorkflowRun, len(candidates))
	uniqueRuns := make(map[string]store.CIWorkflowRun)
	for _, worker := range candidates {
		if worker.WorkflowRunAttempt <= 0 {
			continue
		}
		repository := normalizeRepository(worker.Repository)
		key := fmt.Sprintf("%s:%d:%d", repository, worker.WorkflowRunID, worker.WorkflowRunAttempt)
		run, ok := attemptRuns[key]
		if !ok {
			continue
		}
		runByWorker[worker.ID] = run
		uniqueRuns[key] = run
	}

	workflowFileCache := map[string]githubrunner.RepositoryFile{}
	for _, worker := range candidates {
		run, ok := runByWorker[worker.ID]
		if !ok || !ciaudit.OrdinaryValidationWorkflow(run.Name) {
			continue
		}
		job, ok := ciaudit.ExecutionFromWorker(worker)
		if !ok {
			continue
		}

		if strings.EqualFold(run.Event, "pull_request") {
			for _, number := range run.PullRequestNumbers {
				pr := prs[fmt.Sprintf("%s#%d", normalizeRepository(worker.Repository), number)]
				if pr.MergedAt.IsZero() {
					continue
				}
				if finding, ok := ciaudit.PostMergeFinding(job, pr.MergedAt, true); ok {
					finding.Evidence = []string{
						fmt.Sprintf("workflow %q is explicitly classified as ordinary validation", run.Name),
						fmt.Sprintf("pull request #%d merged at %s", number, pr.MergedAt.UTC().Format(time.RFC3339)),
						fmt.Sprintf("workflow run %d attempt %d continued after merge", run.RunID, run.Attempt),
					}
					if err := findingStore.UpsertCIFinding(ctx, normalizeRepository(worker.Repository), finding); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}

		if supersededAt, newer, ok := supersedingRun(run, uniqueRuns, worker.CompletedAt); ok {
			if finding, ok := ciaudit.SupersededFinding(job, supersededAt, true); ok {
				finding.Evidence = []string{
					fmt.Sprintf("workflow %q is explicitly classified as ordinary validation", run.Name),
					fmt.Sprintf("run %d (%s) was superseded by run %d (%s)", run.RunID, run.HeadSHA, newer.RunID, newer.HeadSHA),
					fmt.Sprintf("newer run was created at %s on branch %s", supersededAt.UTC().Format(time.RFC3339), run.HeadBranch),
				}
				if err := findingStore.UpsertCIFinding(ctx, normalizeRepository(worker.Repository), finding); err != nil {
					errs = append(errs, err)
				}
			}
		}

		if certificatesOK {
			if err := r.reconcileValidationIdentity(ctx, worker, run, certificateStore, findingStore, workflowFileCache); err != nil {
				errs = append(errs, fmt.Errorf("%s worker %s validation identity: %w", normalizeRepository(worker.Repository), worker.ID, err))
			}
		}
	}

	return errors.Join(errs...)
}

func (r *Reconciler) findWorkerAttempt(ctx context.Context, scope core.GitHubScope, worker core.Worker, latest githubrunner.WorkflowRunMetadata, cache map[string][]githubrunner.WorkflowJob) (int, *githubrunner.WorkflowJob, error) {
	if latest.Attempt <= 0 {
		return 0, nil, errors.New("GitHub returned a workflow run without a positive attempt")
	}
	wantedID, _ := strconv.ParseInt(strings.TrimSpace(worker.JobID), 10, 64)
	for attempt := 1; attempt <= latest.Attempt; attempt++ {
		key := fmt.Sprintf("%s/%s:%d:%d", scope.Owner, scope.Repository, worker.WorkflowRunID, attempt)
		jobs, ok := cache[key]
		if !ok {
			var err error
			jobs, err = r.GitHub.ListWorkflowJobsAttempt(ctx, scope, worker.WorkflowRunID, attempt)
			if err != nil {
				return 0, nil, err
			}
			cache[key] = jobs
		}
		for i := range jobs {
			matchID := wantedID > 0 && jobs[i].ID == wantedID
			matchRunner := strings.TrimSpace(worker.RunnerName) != "" && jobs[i].RunnerName == worker.RunnerName
			if matchID || matchRunner {
				job := jobs[i]
				return attempt, &job, nil
			}
		}
	}
	if latest.Attempt == 1 {
		return 1, nil, nil
	}
	return 0, nil, nil
}

func supersedingRun(current store.CIWorkflowRun, runs map[string]store.CIWorkflowRun, completedAt time.Time) (time.Time, store.CIWorkflowRun, bool) {
	event := strings.ToLower(strings.TrimSpace(current.Event))
	if event != "push" && event != "pull_request" {
		return time.Time{}, store.CIWorkflowRun{}, false
	}
	if strings.TrimSpace(current.HeadBranch) == "" || strings.TrimSpace(current.Path) == "" || current.CreatedAt.IsZero() {
		return time.Time{}, store.CIWorkflowRun{}, false
	}
	var best store.CIWorkflowRun
	for _, candidate := range runs {
		if candidate.Repository != current.Repository || candidate.RunID == current.RunID {
			continue
		}
		if !ciaudit.OrdinaryValidationWorkflow(candidate.Name) || !strings.EqualFold(candidate.Name, current.Name) {
			continue
		}
		if strings.TrimSpace(candidate.Path) != strings.TrimSpace(current.Path) ||
			!strings.EqualFold(candidate.Event, current.Event) ||
			candidate.HeadBranch != current.HeadBranch ||
			candidate.HeadSHA == "" || candidate.HeadSHA == current.HeadSHA ||
			!candidate.CreatedAt.After(current.CreatedAt) ||
			!candidate.CreatedAt.Before(completedAt) {
			continue
		}
		if event == "pull_request" && !samePullRequest(current.PullRequestNumbers, candidate.PullRequestNumbers) {
			continue
		}
		if best.RunID == 0 || candidate.CreatedAt.Before(best.CreatedAt) {
			best = candidate
		}
	}
	if best.RunID == 0 {
		return time.Time{}, store.CIWorkflowRun{}, false
	}
	return best.CreatedAt, best, true
}

func samePullRequest(a, b []int) bool {
	for _, x := range a {
		if x <= 0 {
			continue
		}
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func normalizeRepository(repository string) string {
	return strings.ToLower(strings.TrimSpace(repository))
}

func repositoryScope(repository string) (core.GitHubScope, bool) {
	owner, repo, ok := strings.Cut(normalizeRepository(repository), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return core.GitHubScope{}, false
	}
	return core.GitHubScope{Kind: core.ScopeRepository, Owner: owner, Repository: repo}, true
}

func truncate(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func runIdentityKey(repository string, runID int64, attempt int) string {
	return fmt.Sprintf("%s:%d:%d", normalizeRepository(repository), runID, attempt)
}
