package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

// Fail loudly if an agent result handler regresses to scanning all worker
// history. Deleted worker history can contain tens of thousands of records.
type resultLookupGuardStore struct {
	store.Store
	listCalls int
	getError  error
}

func (s *resultLookupGuardStore) ListWorkers(context.Context) ([]core.Worker, error) {
	s.listCalls++
	return nil, errors.New("unexpected full worker-history scan")
}

func (s *resultLookupGuardStore) GetWorker(ctx context.Context, id string) (core.Worker, error) {
	if s.getError != nil {
		return core.Worker{}, s.getError
	}
	return s.Store.GetWorker(ctx, id)
}

func TestAgentResultsAuthenticateByExactWorkerLookup(t *testing.T) {
	ctx := context.Background()
	raw, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	st := &resultLookupGuardStore{Store: raw}
	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := hub.Enroll(ctx, "0123456789abcdef", agent.EnrollRequest{Name: "owner", Provider: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := hub.Enroll(ctx, "0123456789abcdef", agent.EnrollRequest{Name: "other", Provider: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "worker-1", HostID: owner.HostID, State: core.WorkerReady}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Enqueue(owner.HostID, agent.Command{
		ID: "command-1", WorkerID: "worker-1", Type: agent.CommandInspect,
	}); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Store: st, Hub: hub}
	post := func(token, workerID string) int {
		t.Helper()
		body := []byte(`{"commandId":"command-1","workerId":"` + workerID + `","ok":true}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/results", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr.Code
	}

	if got := post(other.AgentToken, "worker-1"); got != http.StatusUnauthorized {
		t.Fatalf("another host's valid token status=%d, want 401", got)
	}
	if got := post(owner.AgentToken, "missing-worker"); got != http.StatusUnauthorized {
		t.Fatalf("missing worker status=%d, want 401", got)
	}
	st.getError = errors.New("store unavailable")
	if got := post(owner.AgentToken, "worker-1"); got != http.StatusInternalServerError {
		t.Fatalf("store failure status=%d, want 500", got)
	}
	st.getError = nil

	if got := post(owner.AgentToken, "worker-1"); got != http.StatusNoContent {
		t.Fatalf("authorized result status=%d, want 204", got)
	}
	rec, err := st.GetCommand(ctx, "command-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.AckedAt.IsZero() {
		t.Fatal("authorized result did not acknowledge the durable command")
	}
	if st.listCalls != 0 {
		t.Fatalf("agent result handler scanned full worker history %d times", st.listCalls)
	}
}
