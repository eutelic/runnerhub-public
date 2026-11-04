//go:build !localtest

package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestCIWorkerCandidatesFilterInSQLiteAndPreserveHistory(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)
	for i := 0; i < 10240; i++ {
		w := core.Worker{ID: fmt.Sprintf("deleted-%05d", i), State: core.WorkerDeleted}
		if err := db.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	candidates := []core.Worker{
		{ID: "recent-completed", Repository: "eutelic/runnerhub", WorkflowRunID: 1, State: core.WorkerDeleted, StartedAt: now.Add(-2 * time.Hour), CompletedAt: now.Add(-time.Hour)},
		{ID: "recent-failed", Repository: "eutelic/runnerhub", WorkflowRunID: 2, State: core.WorkerFailed, StartedAt: now.Add(-90 * time.Minute), CompletedAt: time.Time{}, FailedAt: now.Add(-30 * time.Minute)},
		{ID: "old-history", Repository: "eutelic/runnerhub", WorkflowRunID: 3, State: core.WorkerDeleted, StartedAt: now.Add(-48 * time.Hour), CompletedAt: now.Add(-47 * time.Hour)},
		{ID: "future-start", Repository: "eutelic/runnerhub", WorkflowRunID: 4, State: core.WorkerBusy, StartedAt: now.Add(time.Hour), CompletedAt: now.Add(2 * time.Hour)},
		{ID: "no-run", Repository: "eutelic/runnerhub", State: core.WorkerCompleted, StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-30 * time.Minute)},
		{ID: "no-repository", WorkflowRunID: 5, State: core.WorkerCompleted, StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-30 * time.Minute)},
		{ID: "invalid-time-order", Repository: "eutelic/runnerhub", WorkflowRunID: 6, State: core.WorkerCompleted, StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-2 * time.Hour)},
	}
	for _, worker := range candidates {
		if err := db.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListCIWorkerCandidates(ctx, cutoff, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidate count=%d, want 2: %+v", len(got), got)
	}
	found := map[string]bool{}
	for _, worker := range got {
		found[worker.ID] = true
	}
	if !found["recent-completed"] || !found["recent-failed"] {
		t.Fatalf("missing recent CI workers: %+v", got)
	}

	rows, err := db.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+ciWorkerCandidatesQuery, cutoff.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	indexUsed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexUsed = indexUsed || strings.Contains(detail, "idx_workers_ci_candidates")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !indexUsed {
		t.Fatal("CI candidate query did not use the expression index")
	}

	all, err := db.ListWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 10240+len(candidates) {
		t.Fatalf("history count=%d, want %d", len(all), 10240+len(candidates))
	}
}
