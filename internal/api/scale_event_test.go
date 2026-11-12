package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestScaleEventUnknownTerminalIsIdempotent(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{Store: st}
	for _, state := range []string{"completed", "canceled", "failed"} {
		t.Run(state, func(t *testing.T) {
			body := `{"poolId":"pool-publisher","runnerName":"runner-missing","state":"` + state + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
			rec := httptest.NewRecorder()

			svc.scaleEvent(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestScaleEventUnknownBusyStillFails(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{Store: st}
	body := `{"poolId":"pool-publisher","runnerName":"runner-missing","state":"busy"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	svc.scaleEvent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

// TestScaleEventTerminalReplayCannotResurrectCleanup verifies that valid late
// completion/cancellation callbacks do not overwrite the terminal outcome,
// restart deletion, or regress cleanup_pending/deleting to completed.
func TestScaleEventTerminalReplayCannotResurrectCleanup(t *testing.T) {
	for _, state := range []string{"completed", "canceled", "failed"} {
		for _, cleanup := range []core.WorkerState{core.WorkerCleanupPending, core.WorkerDeleting} {
			t.Run(string(cleanup)+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				st, err := store.OpenSQLite(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				updated := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
				completed := updated.Add(-time.Minute)
				original := core.Worker{
					ID: "worker-dedup", PoolID: "pool-dedup", RunnerName: "runner-dedup",
					Scope:      core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
					Repository: "eutelic/runnerhub", WorkflowRef: ".github/workflows/ci.yml",
					WorkflowRunID: 42, JobID: "123", JobResult: "succeeded",
					ProviderHandle: "owned-container", State: cleanup, UpdatedAt: updated, CompletedAt: completed,
				}
				if err := st.UpsertWorker(ctx, original); err != nil {
					t.Fatal(err)
				}
				svc := &Service{Store: st}
				body := fmt.Sprintf(`{"poolId":"pool-dedup","runnerName":"runner-dedup","state":%q,"repository":"eutelic/runnerhub","workflowRef":".github/workflows/ci.yml","workflowRunId":42,"jobId":"123","result":"failed"}`, state)
				for i := 0; i < 3; i++ {
					req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
					rec := httptest.NewRecorder()
					svc.scaleEvent(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("replay %d status=%d body=%s", i, rec.Code, rec.Body.String())
					}
				}
				got, err := st.GetWorker(ctx, original.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.State != cleanup || got.JobResult != original.JobResult ||
					!got.CompletedAt.Equal(completed) || !got.UpdatedAt.Equal(updated) || got.ProviderHandle != original.ProviderHandle {
					t.Fatalf("replayed terminal event changed durable cleanup state: %#v", got)
				}
			})
		}
	}
}

func TestScaleEventCleanupReplayRejectsConflictingIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wk := core.Worker{
		ID: "worker-auth", PoolID: "pool-auth", RunnerName: "runner-auth",
		State: core.WorkerCleanupPending, Repository: "eutelic/runnerhub",
		WorkflowRef: ".github/workflows/ci.yml", WorkflowRunID: 42, JobID: "123",
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, wk); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	for _, mutation := range []string{
		`"jobId":"124"`, `"workflowRunId":43`,
		`"repository":"outside/other"`, `"workflowRef":"other.yml"`,
	} {
		body := `{"poolId":"pool-auth","runnerName":"runner-auth","state":"completed",` + mutation + `}`
		rec := httptest.NewRecorder()
		svc.scaleEvent(rec, httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body)))
		if rec.Code != http.StatusConflict {
			t.Fatalf("mismatch %s status=%d body=%s", mutation, rec.Code, rec.Body.String())
		}
	}
	got, err := st.GetWorker(ctx, wk.ID)
	if err != nil || got.State != core.WorkerCleanupPending {
		t.Fatalf("identity mismatch mutated worker: %+v %v", got, err)
	}
}

func TestScaleEventConcurrentTerminalReplaysRemainCleanupPending(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	original := core.Worker{
		ID: "worker-race", PoolID: "pool-race", RunnerName: "runner-race",
		State: core.WorkerCleanupPending, WorkflowRunID: 42, JobID: "123",
		JobResult: "succeeded", UpdatedAt: time.Now().UTC(),
	}
	if err := st.UpsertWorker(ctx, original); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	const count = 16
	results := make(chan int, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			svc.scaleEvent(rec, httptest.NewRequest(http.MethodPost, "/api/v1/scale-events",
				strings.NewReader(`{"poolId":"pool-race","runnerName":"runner-race","state":"completed","workflowRunId":42,"jobId":"123","result":"failure"}`)))
			results <- rec.Code
		}()
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Errorf("concurrent callback status=%d", code)
		}
	}
	got, err := st.GetWorker(ctx, original.ID)
	if err != nil || got.State != core.WorkerCleanupPending || got.JobResult != original.JobResult {
		t.Fatalf("concurrent callbacks regressed cleanup: %#v %v", got, err)
	}
}

func TestScaleEventLateStartedDoesNotReopenCleanup(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	before := time.Now().UTC().Add(-time.Minute)
	wk := core.Worker{
		ID: "worker-late-start", PoolID: "pool-late-start", RunnerName: "runner-late-start",
		State: core.WorkerCleanupPending, Repository: "eutelic/runnerhub",
		WorkflowRef: ".github/workflows/ci.yml", JobID: "123", WorkflowRunID: 42,
		JobResult: "succeeded", CompletedAt: before, UpdatedAt: before,
	}
	if err := st.UpsertWorker(ctx, wk); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	body := `{"poolId":"pool-late-start","runnerName":"runner-late-start","state":"busy","repository":"eutelic/runnerhub","workflowRef":".github/workflows/ci.yml","workflowRunId":42,"jobId":"123"}`
	for i := 0; i < 2; i++ {
		r := httptest.NewRecorder()
		svc.scaleEvent(r, httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body)))
		if r.Code != http.StatusOK {
			t.Fatalf("late started callback status=%d body=%s", r.Code, r.Body.String())
		}
	}
	got, err := st.GetWorker(ctx, wk.ID)
	if err != nil || got.State != core.WorkerCleanupPending || !got.CompletedAt.Equal(before) || got.JobResult != wk.JobResult || !got.StartedAt.IsZero() {
		t.Fatalf("late start mutated cleaned-up job: %+v %v", got, err)
	}
	for _, mismatch := range []string{
		`{"poolId":"pool-late-start","runnerName":"runner-late-start","state":"busy","workflowRunId":42,"jobId":"other"}`,
		`{"poolId":"pool-late-start","runnerName":"runner-late-start","state":"busy","workflowRunId":41,"jobId":"123"}`,
		`{"poolId":"pool-late-start","runnerName":"runner-late-start","state":"busy"}`,
	} {
		r := httptest.NewRecorder()
		svc.scaleEvent(r, httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(mismatch)))
		if r.Code != http.StatusConflict {
			t.Fatalf("unverified started event status=%d body=%s", r.Code, r.Body.String())
		}
	}
}
