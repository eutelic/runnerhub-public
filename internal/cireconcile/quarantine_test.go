//go:build !localtest

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

type quarantineTestGitHub struct {
	fakeGitHub
	runErrors map[int64]error
	runCalls  map[int64]int
}

func (f *quarantineTestGitHub) GetWorkflowRun(ctx context.Context, scope core.GitHubScope, runID int64) (githubrunner.WorkflowRunMetadata, error) {
	f.runCalls[runID]++
	if err := f.runErrors[runID]; err != nil {
		return githubrunner.WorkflowRunMetadata{}, err
	}
	return f.fakeGitHub.GetWorkflowRun(ctx, scope, runID)
}

func TestPermanentWorkflowRunFailureIsQuarantinedAndRecovers(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	const inaccessibleRunID int64 = 37333906258
	const inaccessibleOtherRunID int64 = 37333906259
	const accessibleRunID int64 = 42

	for _, worker := range []core.Worker{
		{
			ID: "sample-worker", Repository: "example-user/sample-worker", WorkflowRunID: inaccessibleRunID,
			WorkflowRunAttempt: 1, StartedAt: now.Add(-30 * time.Minute), CompletedAt: now.Add(-29 * time.Minute),
		},
		{
			ID: "sample-worker-other", Repository: "example-user/sample-worker", WorkflowRunID: inaccessibleOtherRunID,
			WorkflowRunAttempt: 1, StartedAt: now.Add(-25 * time.Minute), CompletedAt: now.Add(-24 * time.Minute),
		},
		{
			ID: "runnerhub", Repository: "eutelic/runnerhub", WorkflowRunID: accessibleRunID,
			WorkflowRunAttempt: 1, StartedAt: now.Add(-20 * time.Minute), CompletedAt: now.Add(-19 * time.Minute),
		},
	} {
		if err := st.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	accessible := githubrunner.WorkflowRunMetadata{
		ID: accessibleRunID, Attempt: 1, Name: "build", Path: ".github/workflows/build.yml",
		Event: "push", Status: "completed", Conclusion: "success", HeadBranch: "main",
		HeadSHA: "accessible-sha", HeadTreeSHA: "accessible-tree", CreatedAt: now.Add(-21 * time.Minute),
	}
	accessibleOther := githubrunner.WorkflowRunMetadata{
		ID: inaccessibleOtherRunID, Attempt: 1, Name: "build", Path: ".github/workflows/build.yml",
		Event: "push", Status: "completed", Conclusion: "success", HeadBranch: "main",
		HeadSHA: "restored-other-sha", HeadTreeSHA: "restored-other-tree", CreatedAt: now.Add(-26 * time.Minute),
	}
	restored := githubrunner.WorkflowRunMetadata{
		ID: inaccessibleRunID, Attempt: 1, Name: "build", Path: ".github/workflows/build.yml",
		Event: "push", Status: "completed", Conclusion: "success", HeadBranch: "main",
		HeadSHA: "restored-sha", HeadTreeSHA: "restored-tree", CreatedAt: now.Add(-31 * time.Minute),
	}
	gh := &quarantineTestGitHub{
		fakeGitHub: fakeGitHub{runs: map[int64]githubrunner.WorkflowRunMetadata{
			accessibleRunID:        accessible,
			inaccessibleRunID:      restored,
			inaccessibleOtherRunID: accessibleOther,
		}},
		runErrors: map[int64]error{
			inaccessibleRunID: fmt.Errorf("github api GET /repos/example-user/sample-worker/actions/runs/%d returned 403: Resource not accessible by integration", inaccessibleRunID),
		},
		runCalls: map[int64]int{},
	}
	rec := &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: nowFn}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.runCalls[inaccessibleRunID] != 1 || gh.runCalls[inaccessibleOtherRunID] != 1 || gh.runCalls[accessibleRunID] != 1 {
		t.Fatalf("first reconciliation calls: inaccessible=%d same-repo-other=%d accessible=%d", gh.runCalls[inaccessibleRunID], gh.runCalls[inaccessibleOtherRunID], gh.runCalls[accessibleRunID])
	}
	quarantine, err := st.GetCIWorkflowRunQuarantine(ctx, "example-user/sample-worker", inaccessibleRunID)
	if err != nil {
		t.Fatal(err)
	}
	if quarantine.Reason != "permission_denied" || quarantine.FailureCount != 1 ||
		!quarantine.RetryAfter.Equal(now.Add(workflowRunQuarantineFirstDelay)) {
		t.Fatalf("quarantine=%+v", quarantine)
	}
	if _, err := st.GetCIWorkflowRunQuarantine(ctx, "eutelic/runnerhub", accessibleRunID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("accessible repository was quarantined: %v", err)
	}

	// A new reconciler instance sees the persisted cooldown and continues other
	// repositories without repeating the forbidden request.
	rec = &Reconciler{Store: st, GitHub: gh, Lookback: 24 * time.Hour, Now: nowFn}
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.runCalls[inaccessibleRunID] != 1 || gh.runCalls[inaccessibleOtherRunID] != 1 {
		t.Fatalf("quarantined run retried before retry-after or valid run was refetched: forbidden=%d same-repo-other=%d", gh.runCalls[inaccessibleRunID], gh.runCalls[inaccessibleOtherRunID])
	}
	if gh.runCalls[accessibleRunID] != 1 {
		t.Fatalf("cached accessible run made an unnecessary API call: calls=%d", gh.runCalls[accessibleRunID])
	}

	// Once access is restored, the bounded recheck stores metadata and clears
	// only that run’s quarantine without requiring manual data deletion.
	delete(gh.runErrors, inaccessibleRunID)
	now = quarantine.RetryAfter
	if err := rec.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.runCalls[inaccessibleRunID] != 2 || gh.runCalls[inaccessibleOtherRunID] != 1 {
		t.Fatalf("restored run calls: forbidden=%d valid same-repo=%d, want only forbidden rechecked", gh.runCalls[inaccessibleRunID], gh.runCalls[inaccessibleOtherRunID])
	}
	if _, err := st.GetCIWorkflowRunQuarantine(ctx, "example-user/sample-worker", inaccessibleRunID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("successful recheck did not clear quarantine: %v", err)
	}
	runs, err := st.ListCIWorkflowRuns(ctx, "example-user/sample-worker", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("restored workflow metadata count=%d, want 2: %+v", len(runs), runs)
	}
	foundRestored := false
	for _, run := range runs {
		if run.RunID == inaccessibleRunID && run.HeadTreeSHA == "restored-tree" {
			foundRestored = true
		}
	}
	if !foundRestored {
		t.Fatalf("restored workflow metadata missing: %+v", runs)
	}
}

func TestPermanentWorkflowRunFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "permission denied", err: errors.New("github api returned 403: Resource not accessible by integration"), want: "permission_denied"},
		{name: "not found", err: githubrunner.ErrNotFound, want: "run_not_found"},
		{name: "rate limit", err: errors.New("github api returned 403: API rate limit exceeded"), want: ""},
		{name: "secondary rate limit", err: errors.New("github api returned 403: secondary rate limit"), want: ""},
		{name: "transient server error", err: errors.New("github api returned 502: bad gateway"), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permanentWorkflowRunMetadataFailure(tt.err); got != tt.want {
				t.Fatalf("classification=%q, want %q", got, tt.want)
			}
		})
	}
	if got := workflowRunQuarantineDelay(1); got != 6*time.Hour {
		t.Fatalf("first quarantine delay=%v, want 6h", got)
	}
	if got := workflowRunQuarantineDelay(20); got != workflowRunQuarantineMaxDelay {
		t.Fatalf("maximum quarantine delay=%v, want %v", got, workflowRunQuarantineMaxDelay)
	}
}
