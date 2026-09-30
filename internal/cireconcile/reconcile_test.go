package cireconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/store"
)

type fakeGitHub struct {
	runs     map[int64]githubrunner.WorkflowRunMetadata
	attempts map[string]githubrunner.WorkflowRunMetadata
	jobs     map[string][]githubrunner.WorkflowJob
	prs      map[int]githubrunner.PullRequestMetadata
	files    map[string]githubrunner.RepositoryFile
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

func (f fakeGitHub) GetRepositoryFile(_ context.Context, _ core.GitHubScope, path, ref string) (githubrunner.RepositoryFile, error) {
	file, ok := f.files[ref+":"+path]
	if !ok {
		return githubrunner.RepositoryFile{}, fmt.Errorf("file %s at %s not found", path, ref)
	}
	return file, nil
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
func (noCallGitHub) GetRepositoryFile(context.Context, core.GitHubScope, string, string) (githubrunner.RepositoryFile, error) {
	return githubrunner.RepositoryFile{}, errors.New("unexpected GetRepositoryFile call")
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
			HeadBranch: "feature", HeadSHA: sha, HeadTreeSHA: "tree-" + sha, CreatedAt: created, PullRequestNumbers: []int{pr},
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
		HeadBranch: "feature", HeadSHA: "ddd", HeadTreeSHA: "tree-ddd", CreatedAt: started.Add(-time.Minute),
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


func TestReconcilePersistsValidationCoverageExclusions(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	workers := []core.Worker{
		{
			ID: "dind", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 700, WorkflowRunAttempt: 1, JobID: "7001", JobName: "test",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerDinD, Image: images.DefaultRunnerImage,
			StartedAt: now.Add(-20 * time.Minute), CompletedAt: now.Add(-19 * time.Minute), JobResult: "success",
		},
		{
			ID: "unclassified", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 701, WorkflowRunAttempt: 1, JobID: "7002", JobName: "build",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerTrusted, Image: images.DefaultRunnerImage,
			StartedAt: now.Add(-10 * time.Minute), CompletedAt: now.Add(-9 * time.Minute), JobResult: "success",
		},
	}
	for _, worker := range workers {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	run := func(id int64, name, sha string, created time.Time) githubrunner.WorkflowRunMetadata {
		return githubrunner.WorkflowRunMetadata{
			ID: id, Attempt: 1, Name: name, Path: ".github/workflows/" + name + ".yml",
			Event: "push", Status: "completed", Conclusion: "success",
			HeadBranch: "main", HeadSHA: sha, HeadTreeSHA: "tree-" + sha, CreatedAt: created,
		}
	}
	gh := fakeGitHub{
		runs: map[int64]githubrunner.WorkflowRunMetadata{
			700: run(700, "ci", "dind-sha", now.Add(-21*time.Minute)),
			701: run(701, "build", "build-sha", now.Add(-11*time.Minute)),
		},
	}
	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}

	dind, err := st.GetWorker(ctx, "dind")
	if err != nil {
		t.Fatal(err)
	}
	if dind.ValidationExclusionAnalysisVersion == "" || dind.ValidationExclusionReason != validationExclusionUnsupportedIsolation {
		t.Fatalf("dind exclusion=%+v", dind)
	}
	unclassified, err := st.GetWorker(ctx, "unclassified")
	if err != nil {
		t.Fatal(err)
	}
	if unclassified.ValidationExclusionAnalysisVersion == "" || unclassified.ValidationExclusionReason != validationExclusionWorkflowUnclassified {
		t.Fatalf("unclassified exclusion=%+v", unclassified)
	}
}

func TestReconcileDetectsEquivalentValidationAcrossDifferentCommitSHAs(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 23, 13, 0, 0, 0, time.UTC)
	if err := st.UpsertHost(ctx, core.Host{
		ID: "host-1", Name: "host-1", State: core.HostOnline, Architecture: "amd64",
	}); err != nil {
		t.Fatal(err)
	}

	firstStart := now.Add(-30 * time.Minute)
	secondStart := now.Add(-10 * time.Minute)
	workers := []core.Worker{
		{
			ID: "pr-job", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 501, WorkflowRunAttempt: 1, JobID: "5001", JobName: "test",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerTrusted, Image: images.DefaultRunnerImage,
			StartedAt: firstStart, CompletedAt: firstStart.Add(2 * time.Minute), JobResult: "success",
			Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		},
		{
			ID: "main-job", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 502, WorkflowRunAttempt: 1, JobID: "5002", JobName: "test",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerTrusted, Image: images.DefaultRunnerImage,
			StartedAt: secondStart, CompletedAt: secondStart.Add(2 * time.Minute), JobResult: "success",
			Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		},
	}
	for _, worker := range workers {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	const workflow = `name: ci
on:
  push:
  pull_request:
permissions:
  contents: read
jobs:
  test:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - run: go test ./...
`
	prRun := githubrunner.WorkflowRunMetadata{
		ID: 501, Attempt: 1, Name: "ci", Path: ".github/workflows/ci.yml",
		Event: "pull_request", Status: "completed", Conclusion: "success",
		HeadBranch: "feature", HeadSHA: "commit-pr", HeadTreeSHA: "same-tree",
		CreatedAt: firstStart.Add(-time.Minute), PullRequestNumbers: []int{21},
	}
	mainRun := githubrunner.WorkflowRunMetadata{
		ID: 502, Attempt: 1, Name: "ci", Path: ".github/workflows/ci.yml",
		Event: "push", Status: "completed", Conclusion: "success",
		HeadBranch: "main", HeadSHA: "commit-main", HeadTreeSHA: "same-tree",
		CreatedAt: secondStart.Add(-time.Minute),
	}
	gh := fakeGitHub{
		runs: map[int64]githubrunner.WorkflowRunMetadata{501: prRun, 502: mainRun},
		attempts: map[string]githubrunner.WorkflowRunMetadata{"501:1": prRun, "502:1": mainRun},
		prs: map[int]githubrunner.PullRequestMetadata{
			21: {Number: 21, State: "open", HeadRef: "feature", HeadSHA: "commit-pr", BaseRef: "main"},
		},
		files: map[string]githubrunner.RepositoryFile{
			"commit-pr:.github/workflows/ci.yml":   {SHA: "workflow-blob", Content: []byte(workflow)},
			"commit-main:.github/workflows/ci.yml": {SHA: "workflow-blob", Content: []byte(workflow)},
		},
	}

	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}

	first, err := st.GetWorker(ctx, "pr-job")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.GetWorker(ctx, "main-job")
	if err != nil {
		t.Fatal(err)
	}
	if first.ValidationKey == "" || first.ValidationKey != second.ValidationKey {
		t.Fatalf("validation keys differ: pr=%q main=%q", first.ValidationKey, second.ValidationKey)
	}
	if first.ValidationInputHash == "" || first.ValidationInputHash != second.ValidationInputHash {
		t.Fatalf("input hashes differ: pr=%q main=%q", first.ValidationInputHash, second.ValidationInputHash)
	}

	findings, err := st.ListCIFindings(ctx, "eutelic/runnerhub", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	foundEquivalent := false
	for _, finding := range findings {
		if finding.JobID == "main-job" && finding.DetectorID == "equivalent-validation" {
			foundEquivalent = true
			if finding.Class != "input-equivalent" {
				t.Fatalf("equivalent finding class=%q", finding.Class)
			}
		}
	}
	if !foundEquivalent {
		t.Fatalf("expected equivalent validation finding, got %+v", findings)
	}
}

func TestReconcileDoesNotReuseWhenWorkflowReadsGitHubSHA(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC)
	if err := st.UpsertHost(ctx, core.Host{
		ID: "host-1", Name: "host-1", State: core.HostOnline, Architecture: "amd64",
	}); err != nil {
		t.Fatal(err)
	}
	for _, worker := range []core.Worker{
		{
			ID: "first", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 601, WorkflowRunAttempt: 1, JobID: "6001", JobName: "test",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerTrusted, Image: images.DefaultRunnerImage,
			StartedAt: now.Add(-30 * time.Minute), CompletedAt: now.Add(-29 * time.Minute), JobResult: "success",
		},
		{
			ID: "second", HostID: "host-1", Repository: "eutelic/runnerhub",
			WorkflowRunID: 602, WorkflowRunAttempt: 1, JobID: "6002", JobName: "test",
			GitHubRunnerOS: "linux", Isolation: core.IsolationContainerTrusted, Image: images.DefaultRunnerImage,
			StartedAt: now.Add(-10 * time.Minute), CompletedAt: now.Add(-9 * time.Minute), JobResult: "success",
		},
	} {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	const workflow = `name: ci
on: [push]
jobs:
  test:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - run: echo "${{ github.sha }}" && go test ./...
`
	run := func(id int64, sha string, created time.Time) githubrunner.WorkflowRunMetadata {
		return githubrunner.WorkflowRunMetadata{
			ID: id, Attempt: 1, Name: "ci", Path: ".github/workflows/ci.yml",
			Event: "push", Status: "completed", Conclusion: "success",
			HeadBranch: "main", HeadSHA: sha, HeadTreeSHA: "same-tree", CreatedAt: created,
		}
	}
	firstRun := run(601, "commit-one", now.Add(-31*time.Minute))
	secondRun := run(602, "commit-two", now.Add(-11*time.Minute))
	gh := fakeGitHub{
		runs: map[int64]githubrunner.WorkflowRunMetadata{601: firstRun, 602: secondRun},
		attempts: map[string]githubrunner.WorkflowRunMetadata{"601:1": firstRun, "602:1": secondRun},
		files: map[string]githubrunner.RepositoryFile{
			"commit-one:.github/workflows/ci.yml": {SHA: "workflow-blob", Content: []byte(workflow)},
			"commit-two:.github/workflows/ci.yml": {SHA: "workflow-blob", Content: []byte(workflow)},
		},
	}
	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: func() time.Time { return now }}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := st.GetWorker(ctx, "first")
	second, _ := st.GetWorker(ctx, "second")
	if first.ValidationKey == "" || second.ValidationKey == "" || first.ValidationKey == second.ValidationKey {
		t.Fatalf("github.sha must separate identities: first=%q second=%q", first.ValidationKey, second.ValidationKey)
	}
	findings, err := st.ListCIFindings(ctx, "eutelic/runnerhub", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.DetectorID == "equivalent-validation" {
			t.Fatalf("unexpected equivalent finding: %+v", finding)
		}
	}
}
