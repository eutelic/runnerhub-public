package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/policy"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Service) workers(w http.ResponseWriter, r *http.Request) {
	var v []core.Worker
	var err error
	if r.URL.Query().Get("excludeDeleted") == "true" {
		if live, ok := s.Store.(store.NonDeletedWorkerReader); ok {
			v, err = live.ListNonDeletedWorkers(r.Context())
		} else {
			// Compatibility for non-SQLite implementations of Store.
			v, err = s.Store.ListWorkers(r.Context())
			if err == nil {
				filtered := make([]core.Worker, 0, len(v))
				for _, worker := range v {
					if worker.State != core.WorkerDeleted {
						filtered = append(filtered, worker)
					}
				}
				v = filtered
			}
		}
	} else {
		v, err = s.Store.ListWorkers(r.Context())
	}
	if err != nil {
		problem(w, 500, err)
		return
	}
	if auth.PrincipalFrom(r.Context()).Role == auth.RoleScaler {
		for i := range v {
			v[i].ProviderDiagnostics = ""
		}
	}
	writeJSON(w, 200, v)
}

type workflowJobsCacheEntry struct {
	Jobs      []githubrunner.WorkflowJob
	ExpiresAt time.Time
}
type workflowJobsFlight struct {
	done chan struct{}
	Jobs []githubrunner.WorkflowJob
	Err  error
}
type workerProgressResponse struct {
	WorkerID       string `json:"workerId"`
	WorkflowRunID  int64  `json:"workflowRunId"`
	GitHubJobID    int64  `json:"githubJobId,omitempty"`
	Status         string `json:"status,omitempty"`
	Conclusion     string `json:"conclusion,omitempty"`
	CompletedSteps int    `json:"completedSteps"`
	TotalSteps     int    `json:"totalSteps"`
	Percent        int    `json:"percent"`
	CurrentStep    string `json:"currentStep,omitempty"`
	Available      bool   `json:"available"`
}

func workflowJobsCacheKey(scope core.GitHubScope, runID int64) string {
	return strings.ToLower(scope.Owner+"/"+scope.Repository) + ":" + strconv.FormatInt(runID, 10)
}
func workflowJobsCacheTTL(jobs []githubrunner.WorkflowJob) time.Duration {
	allTerminal := len(jobs) > 0
	for _, job := range jobs {
		if !strings.EqualFold(strings.TrimSpace(job.Status), "completed") {
			allTerminal = false
			break
		}
	}
	if allTerminal {
		return 10 * time.Minute
	}
	return 30 * time.Second
}
func (s *Service) cachedWorkflowJobs(ctx context.Context, scope core.GitHubScope, runID int64) ([]githubrunner.WorkflowJob, error) {
	backend, ok := s.GitHub.(githubrunner.WorkflowJobsBackend)
	if !ok {
		return nil, errors.New("github workflow job progress is not configured")
	}
	key := workflowJobsCacheKey(scope, runID)
	now := time.Now().UTC()
	s.workflowJobsMu.Lock()
	if entry, ok := s.workflowJobsCache[key]; ok && entry.ExpiresAt.After(now) {
		jobs := append([]githubrunner.WorkflowJob(nil), entry.Jobs...)
		s.workflowJobsMu.Unlock()
		return jobs, nil
	}
	if pending, ok := s.workflowJobsPending[key]; ok {
		done := pending.done
		s.workflowJobsMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
		return append([]githubrunner.WorkflowJob(nil), pending.Jobs...), pending.Err
	}
	if s.workflowJobsPending == nil {
		s.workflowJobsPending = map[string]*workflowJobsFlight{}
	}
	pending := &workflowJobsFlight{done: make(chan struct{})}
	s.workflowJobsPending[key] = pending
	s.workflowJobsMu.Unlock()
	jobs, err := backend.ListWorkflowJobs(ctx, scope, runID)
	s.workflowJobsMu.Lock()
	pending.Jobs = append([]githubrunner.WorkflowJob(nil), jobs...)
	pending.Err = err
	if err == nil {
		if s.workflowJobsCache == nil {
			s.workflowJobsCache = map[string]workflowJobsCacheEntry{}
		}
		s.workflowJobsCache[key] = workflowJobsCacheEntry{Jobs: append([]githubrunner.WorkflowJob(nil), jobs...), ExpiresAt: time.Now().UTC().Add(workflowJobsCacheTTL(jobs))}
	}
	delete(s.workflowJobsPending, key)
	close(pending.done)
	s.workflowJobsMu.Unlock()
	return append([]githubrunner.WorkflowJob(nil), jobs...), err
}
func matchWorkflowJob(wk core.Worker, jobs []githubrunner.WorkflowJob) (githubrunner.WorkflowJob, bool) {
	return githubrunner.MatchAssignedWorkflowJob(wk.RunnerName, wk.JobName, wk.JobID, jobs)
}
func summarizeWorkflowJob(wk core.Worker, job githubrunner.WorkflowJob) workerProgressResponse {
	out := workerProgressResponse{WorkerID: wk.ID, WorkflowRunID: wk.WorkflowRunID, GitHubJobID: job.ID, Status: job.Status, Conclusion: job.Conclusion, TotalSteps: len(job.Steps), Available: true}
	for _, step := range job.Steps {
		status := strings.ToLower(strings.TrimSpace(step.Status))
		if status == "completed" || strings.TrimSpace(step.Conclusion) != "" {
			out.CompletedSteps++
		}
		if out.CurrentStep == "" && status == "in_progress" {
			out.CurrentStep = step.Name
		}
	}
	if out.TotalSteps > 0 {
		out.Percent = int(float64(out.CompletedSteps)*100/float64(out.TotalSteps) + 0.5)
	}
	if out.CurrentStep == "" && strings.EqualFold(job.Status, "completed") {
		out.CurrentStep = "complete"
	}
	return out
}
func (s *Service) workerProgress(w http.ResponseWriter, r *http.Request) {
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	if _, ok := s.GitHub.(githubrunner.WorkflowJobsBackend); !ok {
		writeJSON(w, 200, []workerProgressResponse{})
		return
	}
	terminal := map[core.WorkerState]bool{core.WorkerCompleted: true, core.WorkerDeleted: true, core.WorkerFailed: true}
	out := []workerProgressResponse{}
	runs := map[string][]core.Worker{}
	scopes := map[string]core.GitHubScope{}
	runIDs := map[string]int64{}
	for _, wk := range workers {
		if terminal[wk.State] || wk.WorkflowRunID <= 0 || wk.Scope.Kind != core.ScopeRepository {
			continue
		}
		key := workflowJobsCacheKey(wk.Scope, wk.WorkflowRunID)
		if wk.WorkflowRunAttempt > 0 {
			key += ":attempt:" + strconv.Itoa(wk.WorkflowRunAttempt)
		}
		runs[key] = append(runs[key], wk)
		scopes[key] = wk.Scope
		runIDs[key] = wk.WorkflowRunID
	}
	for key, group := range runs {
		if len(group) == 0 {
			continue
		}
		var jobs []githubrunner.WorkflowJob
		var err error
		if group[0].WorkflowRunAttempt > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			jobs, err = githubrunner.AssignedWorkflowJobs(ctx, s.GitHub, group[0])
			cancel()
		} else {
			jobs, err = s.cachedWorkflowJobs(r.Context(), scopes[key], runIDs[key])
		}
		if err != nil {
			continue
		}
		for _, wk := range group {
			if job, ok := matchWorkflowJob(wk, jobs); ok {
				out = append(out, summarizeWorkflowJob(wk, job))
			}
		}
	}
	writeJSON(w, 200, out)
}

type createWorkerRequest struct {
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	Public     bool   `json:"public"`
}

func (s *Service) createWorker(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		problem(w, 503, errors.New("github backend is not configured"))
		return
	}
	s.workerScheduleMu.Lock()
	defer s.workerScheduleMu.Unlock()
	if err := s.requireAdmissionOpen(r.Context()); err != nil {
		problem(w, 409, err)
		return
	}
	pool, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	if _, err := s.requireSafeSecurity(r.Context(), pool); err != nil {
		problem(w, 403, err)
		return
	}
	var in createWorkerRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Repository == "" && pool.Scope.Kind == core.ScopeRepository {
		in.Repository = pool.Scope.Owner + "/" + pool.Scope.Repository
	}
	if err := s.Policy.Evaluate(policy.Request{Pool: pool, Repository: in.Repository, Workflow: in.Workflow, Public: in.Public}); err != nil {
		problem(w, 403, err)
		return
	}
	decision, err := s.Scheduler.SelectHost(r.Context(), pool)
	if err != nil {
		problem(w, 409, err)
		return
	}
	id := "worker-" + randomID()
	runnerName := pool.Name + "-" + strings.TrimPrefix(id, "worker-")
	jit, err := s.GitHub.GenerateJITConfig(r.Context(), githubrunner.JITConfigRequest{Scope: pool.Scope, RunnerName: runnerName, RunnerGroupID: pool.RunnerGroupID, Labels: pool.Labels})
	if err != nil {
		problem(w, 502, err)
		return
	}
	now := time.Now().UTC()
	aggregateResources := pool.Isolation == core.IsolationContainerDinD && scheduler.HostSupportsAggregateResourceGroups(decision.Host)
	worker := core.Worker{ID: id, HostID: decision.Host.ID, PoolID: pool.ID, RunnerName: jit.RunnerName, GitHubRunnerID: jit.RunnerID, State: core.WorkerProvisioning, ProviderID: decision.Host.Provider, Isolation: pool.Isolation, Scope: pool.Scope, Labels: pool.Labels, Resources: pool.Resources, AggregateResources: aggregateResources, Image: pool.Image, Repository: in.Repository, WorkflowRef: in.Workflow, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.UpsertWorker(r.Context(), worker); err != nil {
		_ = s.GitHub.DeleteRunner(r.Context(), pool.Scope, jit.RunnerID)
		problem(w, 500, err)
		return
	}
	spec := provider.WorkerSpec{WorkerID: id, PoolID: pool.ID, RunnerName: jit.RunnerName, JITConfig: jit.EncodedConfig, Labels: pool.Labels, Isolation: pool.Isolation, Resources: pool.Resources, AggregateResources: aggregateResources, Image: pool.Image, Metadata: map[string]string{"repository": in.Repository, "workflow": in.Workflow}}
	if err := s.Hub.Enqueue(worker.HostID, agent.Command{Type: agent.CommandCreate, WorkerID: id, Provider: worker.ProviderID, Spec: &spec}); err != nil {
		worker.State = core.WorkerFailed
		worker.FailureReason = truncate(err.Error())
		worker.FailedAt = time.Now().UTC()
		worker.UpdatedAt = worker.FailedAt
		_ = s.Store.UpsertWorker(r.Context(), worker)
		delErr := s.GitHub.DeleteRunner(r.Context(), pool.Scope, jit.RunnerID)
		detail := worker.FailureReason
		if delErr != nil {
			detail += "; github cleanup: " + truncate(delErr.Error())
		}
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "request_worker", ResourceType: "worker", ResourceID: id, Result: "error", SafeDetail: truncate(detail)})
		problem(w, 500, err)
		return
	}
	if s.Metrics != nil {
		s.Metrics.RunnerRequested()
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "request_worker", ResourceType: "worker", ResourceID: id, Result: "ok", SafeDetail: decision.Reason})
	writeJSON(w, 202, worker)
}

type createScaleWorkerRequest struct {
	RunnerName     string `json:"runnerName"`
	JITConfig      string `json:"jitConfig"`
	GitHubRunnerID int64  `json:"githubRunnerId,omitempty"`
	Repository     string `json:"repository,omitempty"`
	Workflow       string `json:"workflow,omitempty"`
}

func (s *Service) createScaleWorker(w http.ResponseWriter, r *http.Request) {
	pool, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	if _, err := s.requireSafeSecurity(r.Context(), pool); err != nil {
		problem(w, 403, err)
		return
	}
	var in createScaleWorkerRequest
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.RunnerName) == "" || strings.TrimSpace(in.JITConfig) == "" {
		problem(w, 400, errors.New("runnerName and jitConfig are required"))
		return
	}
	if in.GitHubRunnerID < 0 {
		problem(w, 400, errors.New("githubRunnerId cannot be negative"))
		return
	}
	if in.Repository == "" && pool.Scope.Kind == core.ScopeRepository {
		in.Repository = pool.Scope.Owner + "/" + pool.Scope.Repository
	}
	if err := s.Policy.Evaluate(policy.Request{Pool: pool, Repository: in.Repository, Workflow: in.Workflow}); err != nil {
		problem(w, 403, err)
		return
	}
	worker, err := s.enqueueWorker(r.Context(), pool, in.RunnerName, in.GitHubRunnerID, in.JITConfig, in.Repository, in.Workflow)
	if err != nil {
		problem(w, 409, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "scale_request_worker", ResourceType: "worker", ResourceID: worker.ID, Result: "ok"})
	writeJSON(w, 202, worker)
}

func validateRegistrationTerminalEvent(wk core.Worker, in scaleEventRequest) error {
	jobID := strings.TrimSpace(in.JobID)
	jobNumber, err := strconv.ParseInt(jobID, 10, 64)
	if err != nil || jobNumber <= 0 {
		return errors.New("registration completion requires a positive GitHub job ID")
	}
	if in.WorkflowRunID <= 0 {
		return errors.New("registration completion requires a GitHub workflow run ID")
	}
	repository := strings.TrimSpace(in.Repository)
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("registration completion requires an owner/repository identity")
	}
	if strings.TrimSpace(in.WorkflowRef) == "" {
		return errors.New("registration completion requires a workflow identity")
	}
	switch wk.Scope.Kind {
	case core.ScopeRepository:
		expected := wk.Scope.Owner + "/" + wk.Scope.Repository
		if !strings.EqualFold(repository, expected) {
			return fmt.Errorf("completion repository %q does not match runner scope %q", repository, expected)
		}
	case core.ScopeOrganization:
		if !strings.EqualFold(parts[0], wk.Scope.Owner) {
			return fmt.Errorf("completion repository %q is outside organization runner scope %q", repository, wk.Scope.Owner)
		}
	default:
		return errors.New("registration completion has an unsupported runner scope")
	}
	if wk.Repository != "" && !strings.EqualFold(repository, wk.Repository) {
		return errors.New("completion repository does not match the worker's recorded job")
	}
	if wk.WorkflowRunID > 0 && in.WorkflowRunID != wk.WorkflowRunID {
		return errors.New("completion workflow run does not match the worker's recorded job")
	}
	if wk.WorkflowRef != "" && !strings.EqualFold(strings.TrimSpace(in.WorkflowRef), strings.TrimSpace(wk.WorkflowRef)) {
		return errors.New("completion workflow does not match the worker's recorded job")
	}
	if wk.JobID != "" && jobID != wk.JobID {
		return errors.New("completion job ID does not match the worker's recorded job")
	}
	return nil
}

type scaleEventRequest struct {
	PoolID        string    `json:"poolId"`
	RunnerName    string    `json:"runnerName"`
	State         string    `json:"state"`
	Detail        string    `json:"detail,omitempty"`
	Repository    string    `json:"repository,omitempty"`
	WorkflowRef   string    `json:"workflowRef,omitempty"`
	WorkflowRunID int64     `json:"workflowRunId,omitempty"`
	JobID         string    `json:"jobId,omitempty"`
	JobName       string    `json:"jobName,omitempty"`
	QueuedAt      time.Time `json:"queuedAt,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	CompletedAt   time.Time `json:"completedAt,omitempty"`
	Result        string    `json:"result,omitempty"`
}

func (s *Service) applyScaleEventState(wk *core.Worker, in scaleEventRequest, state string, wasStarted bool) error {
	switch state {
	case "started", "busy":
		if wk.RegisteredAt.IsZero() {
			wk.RegisteredAt = time.Now().UTC()
		}
		if wk.StartedAt.IsZero() {
			wk.StartedAt = time.Now().UTC()
		}
		if wk.State == core.WorkerRegistering {
			wk.State = core.WorkerReady
		}
		if wk.State != core.WorkerBusy {
			if !core.CanTransitionWorker(wk.State, core.WorkerBusy) {
				return fmt.Errorf("invalid worker transition %s -> busy", wk.State)
			}
			wk.State = core.WorkerBusy
		}
		if s.Metrics != nil && !wasStarted && !wk.QueuedAt.IsZero() && !wk.StartedAt.IsZero() && !wk.StartedAt.Before(wk.QueuedAt) {
			s.Metrics.Queue(wk.PoolID, wk.StartedAt.Sub(wk.QueuedAt))
		}
	case "completed", "canceled":
		if wk.CompletedAt.IsZero() {
			wk.CompletedAt = time.Now().UTC()
		}
		if wk.State != core.WorkerCompleted {
			if wk.State == core.WorkerRegistering {
				// A trusted scale-set completion may arrive before RunnerHub
				// observes the started callback. The handler verified the complete
				// pool, runner, repository, workflow, run, and job identity above;
				// keep this exception local to the authenticated event path.
				wk.State = core.WorkerCompleted
			} else {
				if !core.CanTransitionWorker(wk.State, core.WorkerCompleted) {
					return fmt.Errorf("invalid worker transition %s -> completed", wk.State)
				}
				wk.State = core.WorkerCompleted
			}
		}
		if wk.JobResult == "" {
			wk.JobResult = state
		}
	case "failed":
		wk.State = core.WorkerFailed
		wk.FailureReason = truncate(in.Detail)
		wk.FailedAt = time.Now().UTC()
		if wk.JobResult == "" {
			wk.JobResult = "failed"
		}
	}
	return nil
}

func (s *Service) scaleEvent(w http.ResponseWriter, r *http.Request) {
	var in scaleEventRequest
	if !decode(w, r, &in) {
		return
	}
	state := strings.ToLower(strings.TrimSpace(in.State))
	switch state {
	case "started", "busy", "completed", "canceled", "failed":
	default:
		problem(w, 400, errors.New("unsupported scale event state"))
		return
	}
	workers, err := s.Store.ListWorkers(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	var wk *core.Worker
	var cleaned *core.Worker
	for i := range workers {
		if workers[i].PoolID != in.PoolID || workers[i].RunnerName != in.RunnerName {
			continue
		}
		c := workers[i]
		if c.State == core.WorkerDeleted {
			if cleaned == nil || c.UpdatedAt.After(cleaned.UpdatedAt) {
				cleaned = &c
			}
			continue
		}
		wk = &c
		break
	}
	if wk == nil {
		terminal := state == "completed" || state == "canceled" || state == "failed"
		if terminal {
			resourceID := strings.TrimSpace(in.RunnerName)
			detail := "late " + state + " for unknown scale-set worker"
			if cleaned != nil {
				resourceID = cleaned.ID
				detail = "late " + state + " after cleanup"
				_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "scale_event", ResourceType: "worker", ResourceID: resourceID, Result: "ignored", SafeDetail: truncate(detail)})
				writeJSON(w, 200, cleaned)
				return
			}
			_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "scale_event", ResourceType: "worker", ResourceID: resourceID, Result: "ignored", SafeDetail: truncate(detail)})
			writeJSON(w, 200, map[string]any{"ignored": true, "runnerName": in.RunnerName, "state": state})
			return
		}
		problem(w, 404, errors.New("scale-set worker not found"))
		return
	}
	terminalEvent := state == "completed" || state == "canceled" || state == "failed"
	if terminalEvent {
		if wk.JobID != "" && in.JobID != "" && strings.TrimSpace(wk.JobID) != strings.TrimSpace(in.JobID) {
			problem(w, 409, errors.New("terminal event job ID does not match the worker"))
			return
		}
		if wk.WorkflowRunID > 0 && in.WorkflowRunID > 0 && wk.WorkflowRunID != in.WorkflowRunID {
			problem(w, 409, errors.New("terminal event workflow run does not match the worker"))
			return
		}
		if wk.State == core.WorkerRegistering && (state == "completed" || state == "canceled") {
			if err := validateRegistrationTerminalEvent(*wk, in); err != nil {
				problem(w, 409, err)
				return
			}
		}
	}
	wasStarted := !wk.StartedAt.IsZero() || wk.State == core.WorkerBusy
	wk.Repository = firstNonEmpty(in.Repository, wk.Repository)
	wk.WorkflowRef = firstNonEmpty(in.WorkflowRef, wk.WorkflowRef)
	if in.WorkflowRunID != 0 {
		wk.WorkflowRunID = in.WorkflowRunID
	}
	wk.JobID = firstNonEmpty(in.JobID, wk.JobID)
	wk.JobName = firstNonEmpty(in.JobName, wk.JobName)
	if !in.QueuedAt.IsZero() {
		wk.QueuedAt = in.QueuedAt
	}
	if !in.StartedAt.IsZero() {
		wk.StartedAt = in.StartedAt
	}
	if !in.CompletedAt.IsZero() {
		wk.CompletedAt = in.CompletedAt
	}
	wk.JobResult = firstNonEmpty(in.Result, wk.JobResult)
	if err := s.applyScaleEventState(wk, in, state, wasStarted); err != nil {
		problem(w, 409, err)
		return
	}

	now := time.Now().UTC()
	wk.CompletedAt = normalizeCompletionTime(wk.StartedAt, wk.CompletedAt, now)
	wk.UpdatedAt = now
	if err := s.Store.UpsertWorker(r.Context(), *wk); err != nil {
		problem(w, 500, err)
		return
	}
	if wk.State == core.WorkerCompleted || wk.State == core.WorkerFailed {
		_ = s.maybeAutoTunePool(r.Context(), wk.PoolID)
		if err := s.enqueueTerminalCleanup(r.Context(), wk); err != nil {
			problem(w, 500, err)
			return
		}
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "scale_event", ResourceType: "worker", ResourceID: wk.ID, Result: "ok", SafeDetail: truncate(in.State)})
	writeJSON(w, 200, wk)
}
func (s *Service) enqueueWorker(ctx context.Context, pool core.RunnerPool, runnerName string, githubRunnerID int64, jitConfig, repository, workflow string) (core.Worker, error) {
	s.workerScheduleMu.Lock()
	if runnerName != "" {
		previous, lookupErr := s.findScaleWorker(ctx, pool.ID, runnerName)
		if lookupErr == nil {
			s.workerScheduleMu.Unlock()
			if sameScaleWorkerIdentity(previous, pool.ID, runnerName, githubRunnerID, repository, workflow) {
				if scheduler.CountsTowardPoolConcurrency(previous.State) {
					return previous, nil
				}
				return core.Worker{}, fmt.Errorf("scale-set runner admission is terminal (%s) for pool %q runner %q", previous.State, pool.ID, runnerName)
			}
			return core.Worker{}, fmt.Errorf("scale-set runner identity conflict for pool %q runner %q", pool.ID, runnerName)
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			s.workerScheduleMu.Unlock()
			return core.Worker{}, fmt.Errorf("lookup prior scale-set admission: %w", lookupErr)
		}
	}
	if err := s.requireAdmissionOpen(ctx); err != nil {
		s.workerScheduleMu.Unlock()
		return core.Worker{}, err
	}
	decision, err := s.Scheduler.SelectHost(ctx, pool)
	if err != nil {
		s.workerScheduleMu.Unlock()
		return core.Worker{}, err
	}
	id := "worker-" + randomID()
	now := time.Now().UTC()
	aggregateResources := pool.Isolation == core.IsolationContainerDinD && scheduler.HostSupportsAggregateResourceGroups(decision.Host)
	worker := core.Worker{ID: id, HostID: decision.Host.ID, PoolID: pool.ID, RunnerName: runnerName, GitHubRunnerID: githubRunnerID, State: core.WorkerProvisioning, ProviderID: decision.Host.Provider, Isolation: pool.Isolation, Scope: pool.Scope, Labels: pool.Labels, Resources: pool.Resources, AggregateResources: aggregateResources, Image: pool.Image, Repository: repository, WorkflowRef: workflow, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.UpsertWorker(ctx, worker); err != nil {
		s.workerScheduleMu.Unlock()
		return core.Worker{}, err
	}
	spec := provider.WorkerSpec{WorkerID: id, PoolID: pool.ID, RunnerName: runnerName, JITConfig: jitConfig, Labels: pool.Labels, Isolation: pool.Isolation, Resources: pool.Resources, AggregateResources: aggregateResources, Image: pool.Image, Metadata: map[string]string{"repository": repository, "workflow": workflow}}
	if err := s.Hub.Enqueue(worker.HostID, agent.Command{Type: agent.CommandCreate, WorkerID: id, Provider: worker.ProviderID, Spec: &spec}); err != nil {
		worker.State = core.WorkerFailed
		worker.FailureReason = truncate(err.Error())
		worker.FailedAt = time.Now().UTC()
		worker.UpdatedAt = worker.FailedAt
		_ = s.Store.UpsertWorker(ctx, worker)
		s.workerScheduleMu.Unlock()
		return core.Worker{}, err
	}
	s.workerScheduleMu.Unlock()
	if s.Metrics != nil {
		s.Metrics.RunnerRequested()
	}
	return worker, nil
}
func (s *Service) repositories(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
		problem(w, 503, errors.New("github catalog is not configured"))
		return
	}
	owner := r.URL.Query().Get("owner")
	repo := r.URL.Query().Get("repo")
	kind := core.ScopeOrganization
	if repo != "" {
		kind = core.ScopeRepository
	}
	v, err := s.Catalog.ListRepositories(r.Context(), core.GitHubScope{Kind: kind, Owner: owner, Repository: repo})
	if err != nil {
		problem(w, 502, err)
		return
	}
	writeJSON(w, 200, v)
}
