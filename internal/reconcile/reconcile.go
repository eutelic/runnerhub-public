package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/example-user/runnerhub/internal/agent"
	"github.com/example-user/runnerhub/internal/agenthub"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/githubrunner"
	"github.com/example-user/runnerhub/internal/provider"
	"github.com/example-user/runnerhub/internal/store"
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
	for i := range workers {
		if err := r.reconcileWorker(ctx, &workers[i]); err != nil {
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

func (r *Reconciler) reconcileWorker(ctx context.Context, w *core.Worker) error {
	if w.State == core.WorkerDeleted {
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
		w.UpdatedAt = now.UTC()
		return r.Store.UpsertWorker(ctx, *w)
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

	if r.GitHub != nil && w.GitHubRunnerID > 0 && (w.State == core.WorkerRegistering || w.State == core.WorkerReady || w.State == core.WorkerBusy) {
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

			next := w.State
			if gr.Busy {
				next = core.WorkerBusy
			} else if gr.Status == "online" && (w.State == core.WorkerRegistering || w.State == core.WorkerBusy) {
				next = core.WorkerReady
			}
			if next != w.State && core.CanTransitionWorker(w.State, next) {
				w.State = next
				changed = true
			}
			if changed {
				w.UpdatedAt = now.UTC()
				if err := r.Store.UpsertWorker(ctx, *w); err != nil {
					return err
				}
			}

		case errors.Is(err, githubrunner.ErrNotFound):
			if w.State != core.WorkerRegistering || now.Sub(w.UpdatedAt) >= registrationGrace {
				w.State = core.WorkerFailed
				w.FailureReason = "exact GitHub runner registration disappeared"
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
