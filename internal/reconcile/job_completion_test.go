package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type terminalWorkflowGH struct {
	missingGH
	jobs   []githubrunner.WorkflowJob
	checks int
	err    error
}

func (g *terminalWorkflowGH) ListWorkflowJobs(_ context.Context, _ core.GitHubScope, _ int64) ([]githubrunner.WorkflowJob, error) {
	g.checks++
	return g.jobs, g.err
}
func olderBusyJob(now time.Time, id string) core.Worker {
	return core.Worker{
		ID: id, PoolID: "publisher-pool", HostID: "host-1", ProviderID: "docker",
		ProviderHandle: "container-" + id,
		RunnerName:     "runner-" + id, JobName: "publish (agent, runnerhub-agent)",
		Scope:         core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
		WorkflowRunID: 41211256199, Repository: "eutelic/runnerhub",
		State: core.WorkerBusy, CreatedAt: now.Add(-48 * time.Hour),
		StartedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-47 * time.Hour),
	}
}
func completedGitHubJob(now time.Time, id string) githubrunner.WorkflowJob {
	return githubrunner.WorkflowJob{
		ID: 124571922351, RunnerName: "runner-" + id,
		Name:   "publish (agent, runnerhub-agent)",
		Status: "completed", Conclusion: "failure",
		CompletedAt: now.Add(-47 * time.Hour),
	}
}
func TestAutoReconcilesMissedPublisherCompletion(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	worker := olderBusyJob(now, "publisher")
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	github := &terminalWorkflowGH{jobs: []githubrunner.WorkflowJob{completedGitHubJob(now, "publisher")}}
	r := &Reconciler{Store: st, GitHub: github, Now: func() time.Time { return now }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerCleanupPending || got.JobResult != "failure" ||
		!strings.Contains(got.FailureReason, "GitHub confirmed terminal") {
		t.Fatalf("reconciled worker=%+v", got)
	}
	if github.checks != 1 {
		t.Fatalf("GitHub checks=%d", github.checks)
	}
	events, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, event := range events {
		if event.Action == "reconcile_github_terminal" && event.ResourceID == worker.ID &&
			strings.Contains(event.SafeDetail, "124571922351") {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("no terminal reconciliation audit: %+v", events)
	}
}

func TestAutoReconciliationRejectsUnverifiedEvidence(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		job       githubrunner.WorkflowJob
		workerAge time.Duration
		calls     int
	}{
		{"still running", githubrunner.WorkflowJob{ID: 1, Status: "in_progress", RunnerName: "runner-w", Name: "publish (agent, runnerhub-agent)"}, 48 * time.Hour, 1},
		{"wrong runner", githubrunner.WorkflowJob{ID: 1, Status: "completed", Conclusion: "failure", RunnerName: "other-runner", Name: "publish (agent, runnerhub-agent)", CompletedAt: now.Add(-time.Hour)}, 48 * time.Hour, 1},
		{"unsettled completion", githubrunner.WorkflowJob{ID: 1, Status: "completed", Conclusion: "success", RunnerName: "runner-w", Name: "publish (agent, runnerhub-agent)", CompletedAt: now.Add(-time.Second)}, 48 * time.Hour, 1},
		{"missing completion timestamp", githubrunner.WorkflowJob{ID: 1, Status: "completed", Conclusion: "success", RunnerName: "runner-w", Name: "publish (agent, runnerhub-agent)"}, 48 * time.Hour, 1},
		{"invalid outcome", githubrunner.WorkflowJob{ID: 1, Status: "completed", Conclusion: "unknown", RunnerName: "runner-w", Name: "publish (agent, runnerhub-agent)", CompletedAt: now.Add(-time.Hour)}, 48 * time.Hour, 1},
		{"job not old enough", githubrunner.WorkflowJob{ID: 1, Status: "completed", Conclusion: "success", RunnerName: "runner-w", Name: "publish (agent, runnerhub-agent)", CompletedAt: now.Add(-time.Hour)}, 10 * time.Minute, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.OpenSQLite(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			worker := olderBusyJob(now, "w")
			worker.StartedAt = now.Add(-tc.workerAge)
			if err := st.UpsertWorker(ctx, worker); err != nil {
				t.Fatal(err)
			}
			github := &terminalWorkflowGH{jobs: []githubrunner.WorkflowJob{tc.job}}
			r := &Reconciler{Store: st, GitHub: github, Now: func() time.Time { return now }}
			if err := r.Once(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetWorker(ctx, worker.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != core.WorkerBusy {
				t.Fatalf("unverified completion changed worker state: %+v", got)
			}
			if github.checks != tc.calls {
				t.Fatalf("GitHub checks=%d want %d", github.checks, tc.calls)
			}
		})
	}
}

func TestAutoReconciliationPollsAtMostOncePerInterval(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	worker := olderBusyJob(now, "w")
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	github := &terminalWorkflowGH{jobs: []githubrunner.WorkflowJob{{ID: 1, RunnerName: worker.RunnerName,
		Name: worker.JobName, Status: "in_progress"}}}
	at := now
	r := &Reconciler{Store: st, GitHub: github, Now: func() time.Time { return at }}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if github.checks != 1 {
		t.Fatalf("unthrottled GitHub checks=%d", github.checks)
	}
	at = now.Add(githubJobCompletionPollInterval + time.Second)
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if github.checks != 2 {
		t.Fatalf("job never polled again: %d", github.checks)
	}
}

func TestAutoReconciliationLimitsQueriesPerTick(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		w := olderBusyJob(now, string(rune('a'+i)))
		if err := st.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	github := &terminalWorkflowGH{err: errors.New("temporary GitHub outage")}
	r := &Reconciler{Store: st, GitHub: github, Now: func() time.Time { return now }}
	// Failures are audited but must not prevent progressing through other
	// reconciliation work or allow unbounded GitHub requests.
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if github.checks != 4 {
		t.Fatalf("query burst=%d, want 4", github.checks)
	}
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if github.checks != 7 {
		t.Fatalf("remaining hosts not checked: %d", github.checks)
	}
}
