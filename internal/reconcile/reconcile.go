package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/store"
)

type Reconciler struct {
	Store             store.Store
	GitHub            githubrunner.Backend
	Hub               *agenthub.Hub
	Interval          time.Duration
	HostStaleAfter    time.Duration
	ProvisionTimeout  time.Duration
	RegistrationGrace time.Duration
	Now               func() time.Time

	githubPollMu   sync.Mutex
	githubLastPoll map[string]time.Time
}

func githubRunnerPollInterval(state core.WorkerState) time.Duration {
	switch state {
	case core.WorkerRegistering:
		return 15 * time.Second
	case core.WorkerReady:
		return 2 * time.Minute
	case core.WorkerBusy:
		return 5 * time.Minute
	default:
		return 0
	}
}

func (r *Reconciler) githubRunnerPollDue(w core.Worker, now time.Time) bool {
	interval := githubRunnerPollInterval(w.State)
	if interval <= 0 {
		return false
	}
	r.githubPollMu.Lock()
	defer r.githubPollMu.Unlock()
	if r.githubLastPoll == nil {
		r.githubLastPoll = map[string]time.Time{}
	}
	last, ok := r.githubLastPoll[w.ID]
	if ok && now.Sub(last) < interval {
		return false
	}
	r.githubLastPoll[w.ID] = now
	return true
}

func (r *Reconciler) clearGitHubPoll(workerID string) {
	r.githubPollMu.Lock()
	delete(r.githubLastPoll, workerID)
	delete(r.githubLastPoll, "job:"+workerID)
	r.githubPollMu.Unlock()
}

func (r *Reconciler) Run(ctx context.Context) error {
	if r.Interval <= 0 {
		r.Interval = 15 * time.Second
	}
	t := time.NewTicker(r.Interval)
	defer t.Stop()

	for {
		if err := r.Once(ctx); err != nil {
			_ = r.Store.AppendAudit(ctx, core.AuditEvent{
				Actor:        "reconciler",
				Action:       "tick",
				ResourceType: "system",
				Result:       "error",
				SafeDetail:   truncate(err.Error()),
			})
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (r *Reconciler) Once(ctx context.Context) error {
	if r.Now == nil {
		r.Now = time.Now
	}

	stale := r.HostStaleAfter
	if stale <= 0 {
		stale = 45 * time.Second
	}
	if r.Hub != nil {
		_ = r.Hub.MarkStale(ctx, stale)
	}

	workers, err := r.Store.ListWorkers(ctx)
	if err != nil {
		return err
	}
	// Bound the initial catch-up burst after a restart. Per-worker polling is
	// independently rate-limited, even when a server has many stale records.
	jobChecksRemaining := 4
	for i := range workers {
		if err := r.reconcileWorker(ctx, &workers[i], &jobChecksRemaining); err != nil {
			_ = r.Store.AppendAudit(ctx, core.AuditEvent{
				Actor:        "reconciler",
				Action:       "reconcile",
				ResourceType: "worker",
				ResourceID:   workers[i].ID,
				Result:       "error",
				SafeDetail:   truncate(err.Error()),
			})
		}
	}
	return nil
}

func (r *Reconciler) reconcileWorker(ctx context.Context, w *core.Worker, jobChecksRemaining *int) error {
	if w.State == core.WorkerDeleted {
		r.clearGitHubPoll(w.ID)
		return nil
	}

	now := r.Now()
	provisionTimeout := r.ProvisionTimeout
	if provisionTimeout <= 0 {
		provisionTimeout = 10 * time.Minute
	}
	registrationGrace := r.RegistrationGrace
	if registrationGrace <= 0 {
		registrationGrace = time.Minute
	}

	if w.State == core.WorkerProvisioning && w.ProviderHandle == "" && now.Sub(w.UpdatedAt) > provisionTimeout {
		if w.GitHubRunnerID > 0 && r.GitHub != nil {
			_ = r.GitHub.DeleteRunner(ctx, w.Scope, w.GitHubRunnerID)
		}
		w.State = core.WorkerFailed
		w.FailureReason = "provisioning timed out before provider handle was recorded"
		w.FailedAt = now.UTC()
		w.UpdatedAt = now.UTC()
		return r.Store.UpsertWorker(ctx, *w)
	}

	// When GitHub conclusively finished an assigned job but RunnerHub missed
	// the terminal callback, reconcile it before any further runner inspection.
	// Elapsed time alone must never turn a job terminal.
	if w.State == core.WorkerBusy && jobChecksRemaining != nil && *jobChecksRemaining > 0 &&
		r.githubJobCompletionPollDue(*w,now) {
		*jobChecksRemaining--
		if err:=r.tryReconcileCompletedGitHubJob(ctx,w,now);err!=nil{return err}
	}

	if w.ProviderHandle != "" && r.Hub != nil && (w.State == core.WorkerRegistering || w.State == core.WorkerReady || w.State == core.WorkerBusy) {
		h := provider.WorkerHandle{ProviderID: w.ProviderHandle, HostID: w.HostID}
		_ = r.Hub.Enqueue(w.HostID, agent.Command{
			Type:     agent.CommandInspect,
			WorkerID: w.ID,
			Provider: w.ProviderID,
			Handle:   &h,
		})
	}

	if r.GitHub != nil && w.GitHubRunnerID > 0 && (w.State == core.WorkerRegistering || w.State == core.WorkerReady || w.State == core.WorkerBusy) && r.githubRunnerPollDue(*w, now) {
		gr, err := r.GitHub.GetRunner(ctx, w.Scope, w.GitHubRunnerID)
		switch {
		case err == nil:
			if gr.ID != w.GitHubRunnerID || gr.Name != w.RunnerName {
				return fmt.Errorf("github runner identity mismatch for %s", w.ID)
			}

			changed := w.GitHubRunnerVersion != gr.Version || w.GitHubRunnerOS != gr.OS || w.GitHubRunnerEphemeral != gr.Ephemeral
			w.GitHubRunnerVersion = gr.Version
			w.GitHubRunnerOS = gr.OS
			w.GitHubRunnerEphemeral = gr.Ephemeral

			if gr.Busy {
				if w.RegisteredAt.IsZero() {
					w.RegisteredAt = now.UTC()
					changed = true
				}
				if w.StartedAt.IsZero() {
					w.StartedAt = now.UTC()
					changed = true
				}
				// GitHub can assign a job immediately after registration, before a
				// reconciliation pass ever observes the runner idle/online. Preserve
				// the lifecycle invariant by advancing through ready in the same pass.
				if w.State == core.WorkerRegistering && core.CanTransitionWorker(w.State, core.WorkerReady) {
					w.State = core.WorkerReady
					changed = true
				}
				if w.State != core.WorkerBusy && core.CanTransitionWorker(w.State, core.WorkerBusy) {
					w.State = core.WorkerBusy
					changed = true
				}
			} else if gr.Status == "online" && (w.State == core.WorkerRegistering || w.State == core.WorkerBusy) {
				if w.RegisteredAt.IsZero() {
					w.RegisteredAt = now.UTC()
					changed = true
				}
				if core.CanTransitionWorker(w.State, core.WorkerReady) {
					w.State = core.WorkerReady
					changed = true
				}
			}
			if changed {
				w.UpdatedAt = now.UTC()
				if err := r.Store.UpsertWorker(ctx, *w); err != nil {
					return err
				}
			}

		case errors.Is(err, githubrunner.ErrNotFound):
			// Ephemeral runners can disappear from GitHub's runner API immediately
			// after finishing their assigned job. Once RunnerHub has observed a job
			// start, let the provider's terminal state decide success vs failure
			// instead of racing GitHub's registration cleanup.
			if w.State == core.WorkerBusy && !w.StartedAt.IsZero() {
				return nil
			}
			registrationStarted := w.RegistrationStartedAt
			if registrationStarted.IsZero() {
				registrationStarted = w.CreatedAt
			}
			if registrationStarted.IsZero() {
				registrationStarted = w.UpdatedAt
			}
			if w.State != core.WorkerRegistering || now.Sub(registrationStarted) >= registrationGrace {
				w.State = core.WorkerFailed
				w.FailureReason = "exact GitHub runner registration disappeared"
				w.FailedAt = now.UTC()
				w.UpdatedAt = now.UTC()
				if err := r.Store.UpsertWorker(ctx, *w); err != nil {
					return err
				}
			}

		default:
			return err
		}
	}

	if w.State != core.WorkerCompleted && w.State != core.WorkerFailed && w.State != core.WorkerCleanupPending {
		return nil
	}
	r.clearGitHubPoll(w.ID)

	if w.GitHubRunnerID > 0 && r.GitHub != nil {
		err := r.GitHub.DeleteRunner(ctx, w.Scope, w.GitHubRunnerID)
		if err != nil && !errors.Is(err, githubrunner.ErrNotFound) {
			return err
		}
	}

	if w.State != core.WorkerCleanupPending && core.CanTransitionWorker(w.State, core.WorkerCleanupPending) {
		w.State = core.WorkerCleanupPending
		w.UpdatedAt = now.UTC()
		if err := r.Store.UpsertWorker(ctx, *w); err != nil {
			return err
		}
	}

	if w.ProviderHandle != "" {
		if r.Hub == nil {
			return nil
		}
		h := provider.WorkerHandle{ProviderID: w.ProviderHandle, HostID: w.HostID}
		return r.Hub.Enqueue(w.HostID, agent.Command{
			Type:     agent.CommandDelete,
			WorkerID: w.ID,
			Provider: w.ProviderID,
			Handle:   &h,
		})
	}

	if core.CanTransitionWorker(w.State, core.WorkerDeleted) {
		w.State = core.WorkerDeleted
		w.UpdatedAt = now.UTC()
		return r.Store.UpsertWorker(ctx, *w)
	}
	return nil
}

func truncate(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
