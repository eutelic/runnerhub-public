//go:build !localtest

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCIMetadataStoreRoundTrip(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)
	run := CIWorkflowRun{
		Repository: "Eutelic/RunnerHub", RunID: 77, Attempt: 1, Name: "ci",
		Path: ".github/workflows/ci.yml", Event: "pull_request", HeadBranch: "feature",
		HeadSHA: "abc", CreatedAt: now.Add(-time.Hour), PullRequestNumbers: []int{12},
	}
	if err := st.UpsertCIWorkflowRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.Conclusion = "success"
	if err := st.UpsertCIWorkflowRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	runs, err := st.ListCIWorkflowRuns(context.Background(), "eutelic/runnerhub", now.Add(-2*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Conclusion != "success" || runs[0].Attempt != 1 {
		t.Fatalf("runs=%+v", runs)
	}

	pr := CIPullRequest{
		Repository: "EUTELIC/RUNNERHUB", Number: 12, State: "closed",
		MergedAt: now.Add(-30 * time.Minute), HeadRef: "feature", HeadSHA: "abc", BaseRef: "main",
	}
	if err := st.UpsertCIPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCIPullRequest(context.Background(), "eutelic/runnerhub", 12)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 12 || got.HeadRef != "feature" || !got.MergedAt.Equal(pr.MergedAt) {
		t.Fatalf("pr=%+v", got)
	}
	if _, err := st.GetCIPullRequest(context.Background(), "eutelic/runnerhub", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing PR err=%v", err)
	}
}
