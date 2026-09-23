package cireconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type fakeGitHub struct {
	runs     map[int64]githubrunner.WorkflowRunMetadata
	attempts map[string]githubrunner.WorkflowRunMetadata
	jobs     map[string][]githubrunner.WorkflowJob
	prs      map[int]githubrunner.PullRequestMetadata
}

func (f fakeGitHub) GetWorkflowRun(_ context.Context, _ core.GitHubScope, runID int64) (githubrunner.WorkflowRunMetadata, error) {
	run, ok := f.runs[runID]
	if !ok {
		return githubrunner.WorkflowRunMetadata{}, fmt.Errorf("run %d not found", runID)
	}
	return run, nil
}

func (f fakeGitHub) GetWorkflowRunAttempt(_ context.Context, _ core.GitHubScope, runID int64, attempt int) (githubrunner.WorkflowRunMetadata, error) {
	run, ok := f.attempts[fmt.Sprintf("%d:%d", runID, attempt)]
	if !ok {
		return githubrunner.WorkflowRunMetadata{}, fmt.Errorf("run %d attempt %d not found", runID, attempt)
	}
	return run, nil
}

func (f fakeGitHub) ListWorkflowJobsAttempt(_ context.Context, _ core.GitHubScope, runID int64, attempt int) ([]githubrunner.WorkflowJob, error) {
	return append([]githubrunner.WorkflowJob(nil), f.jobs[fmt.Sprintf("%d:%d", runID, attempt)]...), nil
}

func (f fakeGitHub) GetPullRequest(_ context.Context, _ core.GitHubScope, number int) (githubrunner.PullRequestMetadata, error) {
	pr, ok := f.prs[number]
	if !ok {
		return githubrunner.PullRequestMetadata{}, fmt.Errorf("PR %d not found", number)
	}
	return pr, nil
}

type noCallGitHub struct{}

func (noCallGitHub) GetWorkflowRun(context.Context, core.GitHubScope, int64) (githubrunner.WorkflowRunMetadata, error) {
	return githubrunner.WorkflowRunMetadata{}, errors.New("unexpected GetWorkflowRun call")
}
func (noCallGitHub) GetWorkflowRunAttempt(context.Context, core.GitHubScope, int64, int) (githubrunner.WorkflowRunMetadata, error) {
	return githubrunner.WorkflowRunMetadata{}, errors.New("unexpected GetWorkflowRunAttempt call")
}
func (noCallGitHub) ListWorkflowJobsAttempt(context.Context, core.GitHubScope, int64, int) ([]githubrunner.WorkflowJob, error) {
	return nil, errors.New("unexpected ListWorkflowJobsAttempt call")
}
func (noCallGitHub) GetPullRequest(context.Context, core.GitHubScope, int) (githubrunner.PullRequestMetadata, error) {
	return githubrunner.PullRequestMetadata{}, errors.New("unexpected GetPullRequest call")
}

func TestReconcilePersistsAttemptsAndDeterministicFindings(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	oldCreated := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 9, 22, 10, 15, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 9, 22, 10, 20, 0, 0, time.UTC)
	publishCreated := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	publishMerged := time.Date(2026, 9, 22, 11, 10, 0, 0, time.UTC)

	workers := []core.Worker{
		{ID: "old", Repository: "eutelic/runnerhub", WorkflowRunID: 100, JobID: "101", RunnerName: "runner-old", StartedAt: oldCreated.Add(time.Minute), CompletedAt: oldCreated.Add(30 * time.Minute), Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}},
		{ID: "new", Repository: "eutelic/runnerhub", WorkflowRunID: 200, JobID: "201", RunnerName: "runner-new", StartedAt: newCreated.Add(time.Minute), CompletedAt: newCreated.Add(25 * time.Minute), Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}},
		{ID: "publish", Repository: "eutelic/runnerhub", WorkflowRunID: 300, JobID: "301", RunnerName: "runner-publish", StartedAt: publishCreated.Add(time.Minute), CompletedAt: publishCreated.Add(30 * time.Minute), Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}},
	}
	for _, worker := range workers {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	run := func(id int64, name, sha string, created time.Time, pr int) githubrunner.WorkflowRunMetadata {
		return githubrunner.WorkflowRunMetadata{
			ID: id, Attempt: 1, Name: name, Path: ".github/workflows/" + name + ".yml",
			Event: "pull_request", Status: "completed", Conclusion: "success",
			HeadBranch: "feature", HeadSHA: sha, CreatedAt: created, PullRequestNumbers: []int{pr},
		}
	}
	oldRun := run(100, "ci", "aaa", oldCreated, 5)
	newRun := run(200, "ci", "bbb", newCreated, 5)
	publishRun := run(300, "publish-images", "ccc", publishCreated, 6)
	gh := fakeGitHub{
		runs: map[int64]githubrunner.WorkflowRunMetadata{100: oldRun, 200: newRun, 300: publishRun},
		attempts: map[string]githubrunner.WorkflowRunMetadata{"100:1": oldRun, "200:1": newRun, "300:1": publishRun},
		jobs: map[string][]githubrunner.WorkflowJob{
			"100:1": {{ID: 101, Name: "core", RunnerName: "runner-old"}},
			"200:1": {{ID: 201, Name: "core", RunnerName: "runner-new"}},
			"300:1": {{ID: 301, Name: "publish", RunnerName: "runner-publish"}},
		},
		prs: map[int]githubrunner.PullRequestMetadata{
			5: {Number: 5, State: "closed", MergedAt: mergedAt, HeadRef: "feature", HeadSHA: "bbb", BaseRef: "main"},
			6: {Number: 6, State: "closed", MergedAt: publishMerged, HeadRef: "feature", HeadSHA: "ccc", BaseRef: "main"},
		},
	}

	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}

	gotOld, err := st.GetWorker(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if gotOld.WorkflowRunAttempt != 1 {
		t.Fatalf("old workflow attempt=%d want 1", gotOld.WorkflowRunAttempt)
	}

	runs, err := st.ListCIWorkflowRuns(ctx, "eutelic/runnerhub", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("stored runs=%d want 3", len(runs))
	}

	findings, err := st.ListCIFindings(ctx, "eutelic/runnerhub", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, finding := range findings {
		counts[finding.JobID+":"+finding.DetectorID]++
		if finding.JobID == "publish" {
			t.Fatalf("publish workflow must not produce confirmed waste: %+v", finding)
		}
	}
	if counts["old:post-merge-validation"] != 1 || counts["old:superseded-validation"] != 1 {
		t.Fatalf("old findings=%v", counts)
	}
	if counts["new:post-merge-validation"] != 1 {
		t.Fatalf("new findings=%v", counts)
	}
	if len(findings) != 3 {
		t.Fatalf("findings=%+v", findings)
	}

	// Completed run/PR metadata is immutable and persisted. A later pass should
	// not need GitHub at all for the same executions.
	cached := &Reconciler{Store: st, GitHub: noCallGitHub{}, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := cached.Once(ctx); err != nil {
		t.Fatalf("cached reconciliation unexpectedly called GitHub: %v", err)
	}
}


func TestReconcileFailedWorkerUsesGitHubCompletionTime(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	failedAt := started.Add(20 * time.Minute)
	mergedAt := started.Add(10 * time.Minute)
	worker := core.Worker{
		ID: "failed", Repository: "eutelic/runnerhub", WorkflowRunID: 400, JobID: "401",
		RunnerName: "runner-failed", StartedAt: started, FailedAt: failedAt,
		Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
	}
	if err := st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	run := githubrunner.WorkflowRunMetadata{
		ID: 400, Attempt: 1, Name: "ci", Path: ".github/workflows/ci.yml",
		Event: "pull_request", Status: "completed", Conclusion: "failure",
		HeadBranch: "feature", HeadSHA: "ddd", CreatedAt: started.Add(-time.Minute),
		PullRequestNumbers: []int{9},
	}
	gh := fakeGitHub{
		runs: map[int64]githubrunner.WorkflowRunMetadata{400: run},
		attempts: map[string]githubrunner.WorkflowRunMetadata{"400:1": run},
		jobs: map[string][]githubrunner.WorkflowJob{
			"400:1": {{ID: 401, Name: "core", RunnerName: "runner-failed", StartedAt: started, CompletedAt: failedAt}},
		},
		prs: map[int]githubrunner.PullRequestMetadata{
			9: {Number: 9, State: "closed", MergedAt: mergedAt, HeadRef: "feature", HeadSHA: "ddd", BaseRef: "main"},
		},
	}
	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, "failed")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkflowRunAttempt != 1 || !got.CompletedAt.Equal(failedAt) {
		t.Fatalf("worker=%+v", got)
	}
	findings, err := st.ListCIFindings(ctx, "eutelic/runnerhub", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].DetectorID != "post-merge-validation" {
		t.Fatalf("findings=%+v", findings)
	}
}
