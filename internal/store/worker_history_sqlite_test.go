//go:build !localtest

package store

import (
	"context"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestSQLiteWorkerHistoryFilteringAndPaging(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	workers := []core.Worker{
		{ID: "worker-a", Repository: "eutelic/runnerhub", PoolID: "standard", HostID: "h1",
			State: core.WorkerDeleted, JobResult: "failure", JobName: "Compile_%", WorkflowRunID: 0,
			FailureReason: "disk full", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "worker-b", Repository: "eutelic/runnerhub", PoolID: "publisher", HostID: "h2",
			State: core.WorkerBusy, JobName: "Publish", WorkflowRunID: 1245, UpdatedAt: now.Add(-time.Minute)},
		{ID: "worker-c", Repository: "another/repo", PoolID: "standard", HostID: "h1",
			State: core.WorkerCompleted, JobResult: "success", JobName: "Verify", UpdatedAt: now},
		{ID: "worker-d", Scope: core.GitHubScope{Owner: "Eutelic", Repository: "RunnerHub"},
			PoolID: "standard", HostID: "h1", State: core.WorkerFailed,
			FailureReason: "registration timeout", UpdatedAt: now.Add(time.Minute)},
	}
	for _, w := range workers {
		if err := s.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 4 || len(all.Items) != 2 || !all.HasMore {
		t.Fatalf("first page: %+v", all)
	}
	next, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 4 || len(next.Items) != 2 || next.HasMore {
		t.Fatalf("last page: %+v", next)
	}
	byRepo, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, Repository: "eutelic/runnerhub"})
	if err != nil {
		t.Fatal(err)
	}
	if byRepo.Total != 3 {
		t.Fatalf("repository fallback and case folding: %+v", byRepo)
	}
	failed, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, State: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 2 {
		t.Fatalf("cleaned failed jobs not included: %+v", failed)
	}
	run, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, Search: "1245"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Total != 1 || run.Items[0].ID != "worker-b" {
		t.Fatalf("run ID search: %+v", run)
	}
	for _, search := range []string{"_", "%"} {
		matched, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, Search: search})
		if err != nil {
			t.Fatal(err)
		}
		if matched.Total != 1 || matched.Items[0].ID != "worker-a" {
			t.Fatalf("wildcard %q must match literally: %+v", search, matched)
		}
	}
	missing, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, Search: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Total != 0 {
		t.Fatalf("missing workflow run IDs matched zero: %+v", missing)
	}
	beyond, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 10, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if beyond.Total != 4 || beyond.Items == nil || len(beyond.Items) != 0 || beyond.HasMore {
		t.Fatalf("empty page: %+v", beyond)
	}
	_, err = s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 101})
	if err == nil {
		t.Fatal("unbounded history page was accepted")
	}
}

func TestSQLiteWorkerHistoryUTCDateBounds(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, w := range []core.Worker{
		{ID: "old", State: core.WorkerCompleted, UpdatedAt: time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)},
		{ID: "first", State: core.WorkerCompleted, UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "last", State: core.WorkerCompleted, UpdatedAt: time.Date(2026, 11, 2, 23, 59, 59, 0, time.UTC)},
		{ID: "new", State: core.WorkerCompleted, UpdatedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)},
		// A missing updatedAt should fall back to the visible createdAt.
		{ID: "created-only", State: core.WorkerRequested, CreatedAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)},
		// An offset-bearing RFC3339 timestamp is normalized to UTC.
		{ID: "offset", State: core.WorkerCompleted, UpdatedAt: time.Date(2026, 11, 2, 23, 30, 0, 0, time.FixedZone("EST", -5*3600))},
	} {
		if err := s.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit: 25, From: "2026-11-01", To: "2026-11-02"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, w := range page.Items {
		ids[w.ID] = true
	}
	for _, id := range []string{"first", "last", "created-only"} {
		if !ids[id] {
			t.Errorf("missing in UTC date window: %s", id)
		}
	}
	for _, id := range []string{"old", "new", "offset"} {
		if ids[id] {
			t.Errorf("outside UTC date window: %s", id)
		}
	}
	if page.Total != 3 {
		t.Fatalf("UTC date window: %+v", page)
	}
}

func TestSQLiteHistorySearchFindsGitHubJobID(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertWorker(context.Background(), core.Worker{
		ID: "work-job-id", JobName: "Build", JobID: "984321654", State: core.WorkerDeleted,
		UpdatedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	matches, err := s.QueryWorkerHistory(context.Background(), WorkerHistoryQuery{
		Limit: 10, Search: "984321654",
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches.Total != 1 || matches.Items[0].ID != "work-job-id" {
		t.Fatalf("GitHub job id search failed: %+v", matches)
	}
}
